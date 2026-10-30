package layout

import (
	"os"
	"path/filepath"
)

// HomeDir is the directory kubectl treats as home, so that kcx and kubectl agree
// on ~/.kube. On Unix it is HOME. On Windows, where HOME is rarely set, it is
// client-go's rule (k8s.io/client-go/util/homedir), here with the environment
// passed in: the first of HOME, HOMEDRIVE+HOMEPATH and USERPROFILE that holds
// .kube\config; else the first of HOME, USERPROFILE and HOMEDRIVE+HOMEPATH that
// is a writable directory, else the first that exists, else the first that is
// set.
func HomeDir(goos string, getenv Getenv) string {
	if goos != "windows" {
		return getenv("HOME")
	}
	home, profile := getenv("HOME"), getenv("USERPROFILE")
	drivePath := ""
	if d, p := getenv("HOMEDRIVE"), getenv("HOMEPATH"); d != "" && p != "" {
		drivePath = d + p
	}
	for _, p := range []string{home, drivePath, profile} {
		if p == "" {
			continue
		}
		if _, err := os.Stat(filepath.Join(p, ".kube", "config")); err == nil {
			return p
		}
	}
	firstSet, firstExisting := "", ""
	for _, p := range []string{home, profile, drivePath} {
		if p == "" {
			continue
		}
		if firstSet == "" {
			firstSet = p
		}
		info, err := os.Stat(p)
		if err != nil {
			continue
		}
		if firstExisting == "" {
			firstExisting = p
		}
		if info.IsDir() && info.Mode().Perm()&0o200 != 0 {
			return p
		}
	}
	if firstExisting != "" {
		return firstExisting
	}
	return firstSet
}
