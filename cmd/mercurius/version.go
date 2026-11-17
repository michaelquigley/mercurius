package main

import "github.com/michaelquigley/push/build"

func init() {
	// mercurius is at v0.2.0; advertise the dev base as v0.2.x for
	// unstamped developer builds.
	build.DevVersion = "v0.2.x"
}
