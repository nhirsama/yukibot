// Package version contains build-time application version information.
package version

// Version is replaced by the build with the Git tag version.
//
// Development builds use the default value unless the linker injects a
// version explicitly.
var Version = "dev"
