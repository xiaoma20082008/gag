package gateway

// 构建期由 scripts/build.sh 通过 -ldflags 注入：
//
//	-X gag/internal/gateway.version=...
//	-X gag/internal/gateway.commit=...
//	-X gag/internal/gateway.buildTime=...
//
// 只用于日志与排障定位，不参与任何业务判断。默认值是"未经构建脚本
// 直接 go run"的场景，能一眼看出这不是正式产物。
var (
	version   = "dev"
	commit    = "unknown"
	buildTime = "unknown"
)

// Build 是构建标识。
type Build struct {
	Version string
	Commit  string
	Time    string
}

// BuildInfo 返回当前二进制的构建标识。
func BuildInfo() Build {
	return Build{Version: version, Commit: commit, Time: buildTime}
}

// String 返回 "version (commit)" 形式；没有 commit 时只返回版本。
func (b Build) String() string {
	if b.Commit == "" || b.Commit == "unknown" {
		return b.Version
	}
	return b.Version + " (" + b.Commit + ")"
}
