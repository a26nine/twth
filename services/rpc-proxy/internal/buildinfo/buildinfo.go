package buildinfo

import "fmt"

var (
	version = "dev"
	commit  = "unknown"
)

type Info struct {
	Version string `json:"version"`
	Commit  string `json:"commit"`
}

func Current() Info {
	return Info{
		Version: version,
		Commit:  commit,
	}
}

func (i Info) String() string {
	return fmt.Sprintf("rpc-proxy version=%s commit=%s", i.Version, i.Commit)
}
