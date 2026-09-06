// Package pack will turn an Astro project directory into the tarball the
// CLI uploads: honoring .gitignore, excluding the build and dependency
// directories no deploy should ship, and enforcing the local file-count
// and size limits before a single byte leaves the machine. Empty for now;
// a later change fills it in.
package pack
