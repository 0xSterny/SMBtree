package smb

import (
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/hirochachacha/go-smb2"
)

// DownloadFile downloads a remote file OR directory to local destination
func (s *Session) DownloadFile(share, remotePath, localDest string) error {
	fs, err := s.session.Mount(share)
	if err != nil {
		return err
	}
	defer fs.Umount()

	// Normalize remotePath to backslashes for SMB usage
	remotePath = strings.ReplaceAll(remotePath, "/", "\\")

	stat, err := fs.Stat(remotePath)
	if err != nil {
		return err
	}

	if stat.IsDir() {
		return s.downloadDirRecursive(fs, remotePath, localDest)
	}

	return s.downloadFileContent(fs, remotePath, localDest)
}

func (s *Session) downloadDirRecursive(fs *smb2.Share, remotePath, localDest string) error {
	// Create local dir
	if err := os.MkdirAll(localDest, 0755); err != nil {
		return err
	}

	infos, err := fs.ReadDir(remotePath)
	if err != nil {
		return err
	}

	for _, info := range infos {
		if info.Name() == "." || info.Name() == ".." {
			continue
		}

		// Construct paths
		// Remote path always uses backslash
		rPath := remotePath + "\\" + info.Name()
		// Local path uses OS separator
		lPath := filepath.Join(localDest, info.Name())

		if info.IsDir() {
			if err := s.downloadDirRecursive(fs, rPath, lPath); err != nil {
				// We return on error, effectively stopping the recursive download
				// Could logging be better? For now, fail fast.
				return err
			}
		} else {
			if err := s.downloadFileContent(fs, rPath, lPath); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Session) downloadFileContent(fs *smb2.Share, remotePath, localDest string) error {
	remoteFile, err := fs.Open(remotePath)
	if err != nil {
		return err
	}
	defer remoteFile.Close()

	if err := os.MkdirAll(filepath.Dir(localDest), 0755); err != nil {
		return err
	}

	localFile, err := os.Create(localDest)
	if err != nil {
		return err
	}
	defer localFile.Close()

	_, err = io.Copy(localFile, remoteFile)
	return err
}
