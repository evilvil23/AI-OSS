package server

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"smart-nas/internal/api/types"
)

type loginRequest struct {
	Username string `json:"username" binding:"required"`
	Password string `json:"password" binding:"required"`
}

// login POST /api/auth/login
func (s *Server) login(c *gin.Context) {
	var req loginRequest
	if !bindJSON(c, &req) {
		return
	}
	res, err := s.deps.Auth.Login(req.Username, req.Password)
	if err != nil {
		c.JSON(http.StatusUnauthorized, types.Fail(types.CodeUnauthorized, "用户名或密码错误"))
		return
	}
	c.JSON(http.StatusOK, types.OK(res))
}

// logout POST /api/auth/logout
// JWT 为无状态设计，服务端仅确认退出（如需加入黑名单可在此扩展）
func (s *Server) logout(c *gin.Context) {
	c.JSON(http.StatusOK, types.OK(map[string]interface{}{"logout": true}))
}

// me GET /api/auth/me
func (s *Server) me(c *gin.Context) {
	uid := currentUID(c)
	u, err := s.deps.Users.GetByID(uid)
	if err != nil {
		c.JSON(http.StatusNotFound, types.Fail(types.CodeFileNotFound, "用户不存在"))
		return
	}
	c.JSON(http.StatusOK, types.OK(u.Public()))
}

type changePasswordRequest struct {
	OldPassword string `json:"old_password" binding:"required"`
	NewPassword string `json:"new_password" binding:"required"`
}

// changePassword POST /api/auth/change-password
func (s *Server) changePassword(c *gin.Context) {
	var req changePasswordRequest
	if !bindJSON(c, &req) {
		return
	}
	if err := s.deps.Auth.ChangePassword(currentUID(c), req.OldPassword, req.NewPassword); err != nil {
		c.JSON(http.StatusBadRequest, types.Fail(types.CodeBadRequest, err.Error()))
		return
	}
	c.JSON(http.StatusOK, types.OK(map[string]interface{}{"changed": true}))
}