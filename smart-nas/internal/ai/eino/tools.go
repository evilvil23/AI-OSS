// tools.go 工具适配：Eino Tool 协议（tool.InvokableTool）+ compose.ToolsNode 执行。
//
// 将 tools.Registry 中的工具包装为 Eino InvokableTool（JSON Schema 参数、
// JSON 字符串入参），由 ToolsNode 统一执行并生成 role=tool 结果消息。
package eino

import (
	"context"
	"encoding/json"

	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
	"github.com/eino-contrib/jsonschema"

	"smart-nas/internal/ai/tools"
)

// 确保 invokableTool 实现 Eino 接口
var _ tool.InvokableTool = (*invokableTool)(nil)

// invokableTool Eino 工具适配器（包装 tools.Tool）
type invokableTool struct {
	name       string
	desc       string
	paramsJSON json.RawMessage
	exec       func(ctx context.Context, argsJSON string) (string, error)
}

// Info 返回 Eino 工具定义（JSON Schema 形式的参数）
func (t *invokableTool) Info(_ context.Context) (*schema.ToolInfo, error) {
	info := &schema.ToolInfo{Name: t.name, Desc: t.desc}
	if len(t.paramsJSON) > 0 {
		var js jsonschema.Schema
		if err := json.Unmarshal(t.paramsJSON, &js); err != nil {
			return nil, err
		}
		info.ParamsOneOf = schema.NewParamsOneOfByJSONSchema(&js)
	}
	return info, nil
}

// InvokableRun 执行工具（Eino Tool 协议）
func (t *invokableTool) InvokableRun(ctx context.Context, argumentsInJSON string, _ ...tool.Option) (string, error) {
	return t.exec(ctx, argumentsInJSON)
}

// AdaptTools 将工具注册表全部工具适配为 Eino 工具列表
func AdaptTools(reg *tools.Registry) []tool.BaseTool {
	out := make([]tool.BaseTool, 0, 8)
	for _, raw := range reg.ToolSchemas() {
		var def struct {
			Function struct {
				Name        string          `json:"name"`
				Description string          `json:"description"`
				Parameters  json.RawMessage `json:"parameters"`
			} `json:"function"`
		}
		if err := json.Unmarshal(raw, &def); err != nil || def.Function.Name == "" {
			continue
		}
		name := def.Function.Name
		t := &invokableTool{
			name:       name,
			desc:       def.Function.Description,
			paramsJSON: def.Function.Parameters,
			exec: func(ctx context.Context, argsJSON string) (string, error) {
				return reg.Execute(ctx, name, json.RawMessage(argsJSON))
			},
		}
		out = append(out, t)
	}
	return out
}

// NewToolsNode 创建绑定工具列表的 ToolsNode
func NewToolsNode(ctx context.Context, toolsList []tool.BaseTool) (*compose.ToolsNode, error) {
	return compose.NewToolNode(ctx, &compose.ToolsNodeConfig{Tools: toolsList})
}
