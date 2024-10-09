package tempfs

import (
	"os"
	"path"
	"path/filepath"

	"github.com/go-git/go-billy/v5"
	"github.com/go-git/go-billy/v5/osfs"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/pkg/errors"
)

// These are the different folders used for local storage.
// /tmp/oss-rebuild/
// 		assets/
//		firestore/

const (
	TempRoot = "/tmp/oss-rebuild/"
)

func FirestoreFS() billy.Filesystem {
	return osfs.New(path.Join(TempRoot, "firestore"))
}

func AssetStore(runID string) (*rebuild.FilesystemAssetStore, error) {
	dir := filepath.Join(TempRoot, "assets", runID)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, errors.Wrapf(err, "failed to create directory %s", dir)
	}
	assetsFS, err := osfs.New("/").Chroot(dir)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to chroot into directory %s", dir)
	}
	return rebuild.NewFilesystemAssetStore(assetsFS), nil
}
