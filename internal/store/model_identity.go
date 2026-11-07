package store

import "time"

var beijingZone = time.FixedZone("CST", 8*3600)

// ModelAliasEffectiveFrom 是旧模型名开始被路由到 V4.1 Flash 的时刻。
var ModelAliasEffectiveFrom = time.Date(2026, time.September, 10, 0, 0, 0, 0, beijingZone)

var modelAliases = map[string]string{
	"deepseek-v4-flash":            "deepseek-flash",
	"deepseek-v4-flash-vision-exp": "deepseek-flash",
}

// ModelDisplayName 只改展示名；usage_log.model 仍保留客户端实际请求的名字。
func ModelDisplayName(model string, at time.Time) string {
	if at.Before(ModelAliasEffectiveFrom) {
		return model
	}
	if canonical, ok := modelAliases[model]; ok {
		return canonical
	}
	return model
}

func IsDeprecatedModelAlias(model string, at time.Time) bool {
	if at.Before(ModelAliasEffectiveFrom) {
		return false
	}
	_, ok := modelAliases[model]
	return ok
}
