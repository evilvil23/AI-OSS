package util

// slice.go 泛型切片/映射工具（Go 1.18+ 泛型）。
// 仅收录项目中存在 ≥2 处真实调用点的操作，避免过度抽象。

// Keys 返回 map 的全部 key（无特定顺序，需要有序请自行 sort）
func Keys[K comparable, V any](m map[K]V) []K {
	out := make([]K, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// Filter 返回满足 predicate 的元素组成的新切片（保持原顺序）
func Filter[T any](s []T, keep func(T) bool) []T {
	out := make([]T, 0, len(s))
	for _, v := range s {
		if keep(v) {
			out = append(out, v)
		}
	}
	return out
}

// Map 将 fn 逐元素映射到新切片（保持原顺序）
func Map[T, R any](s []T, fn func(T) R) []R {
	out := make([]R, len(s))
	for i, v := range s {
		out[i] = fn(v)
	}
	return out
}
