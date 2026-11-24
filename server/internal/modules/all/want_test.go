package all

// wantModules are the modules this build's tags keep, in load order: each module's want_<id>_test.go adds its id
// under the module's build constraint.
var wantModules []string
