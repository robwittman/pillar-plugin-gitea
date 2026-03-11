package main

import (
	"github.com/robwittman/pillar/pkg/plugin"
)

func main() {
	plugin.Serve(&giteaPlugin{})
}
