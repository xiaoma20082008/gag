package gateway

// 构建期由 scripts/build.sh 通过 -ldflags 注入：
//
//	-X gag/internal/gateway.version=...
//	-X gag/internal/gateway.commit=...
//	-X gag/internal/gateway.buildTime=...

var (
	version   = "dev"
	commit    = "unknown"
	buildTime = "unknown"
)

type Build struct {
	Version string
	Commit  string
	Time    string
}

func BuildInfo() Build {
	return Build{Version: version, Commit: commit, Time: buildTime}
}

func (b Build) String() string {
	if b.Commit == "" || b.Commit == "unknown" {
		return b.Version
	}
	return b.Version + " (" + b.Commit + ")"
}
