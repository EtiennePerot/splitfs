package split

import (
	"errors"
	"fmt"
	"hash/fnv"
	"io/ioutil"
	"math/big"
	"os"
	"path"
	"path/filepath"
	"syscall"
	"time"

	"bazil.org/fuse"
	"bazil.org/fuse/fs"
	"golang.org/x/net/context"
)

type splitFS struct {
	sourceDirectory string
	chunkSize       *big.Int
}

var _ fs.FS = (*splitFS)(nil)

func (f *splitFS) Root() (fs.Node, error) {
	return &directory{&node{f, ""}}, nil
}

func NewFS(sourceDirectory string, chunkSize int64) (fs.FS, error) {
	if chunkSize <= 0 {
		return nil, fmt.Errorf("chunksize (%d bytes) must be larger than 0", chunkSize)
	}
	sourceStat, err := os.Stat(sourceDirectory)
	if err != nil {
		return nil, fmt.Errorf("source %q: cannot stat: %v", sourceDirectory, err)
	}
	if !sourceStat.Mode().IsDir() {
		return nil, fmt.Errorf("%q: not a directory", sourceDirectory)
	}
	absoluteSource, err := filepath.Abs(sourceDirectory)
	if err != nil {
		return nil, fmt.Errorf("cannot convert %q to absolute directory: %v", sourceDirectory, err)
	}
	return &splitFS{
		sourceDirectory: absoluteSource,
		chunkSize:       big.NewInt(chunkSize),
	}, nil
}

type node struct {
	splitFS          *splitFS
	rootRelativePath string
}

func (n *node) FullPath() string {
	return path.Join(n.splitFS.sourceDirectory, n.rootRelativePath)
}

type directory struct {
	*node
}

var _ fs.Node = (*directory)(nil)
var _ fs.Handle = (*directory)(nil)
var _ fs.HandleReadDirAller = (*directory)(nil)

func convertTime(timespec syscall.Timespec) time.Time {
	sec, nsec := timespec.Unix()
	return time.Unix(sec, nsec)
}

func osToFuseErr(err error) error {
	if os.IsNotExist(err) {
		return fuse.ENOENT
	}
	if os.IsPermission(err) {
		return fuse.EPERM
	}
	return fuse.ENOSYS
}

func copyStatToAttr(stat *syscall.Stat_t, attr *fuse.Attr) {
	mode := os.FileMode(stat.Mode & 0777)
	switch stat.Mode & syscall.S_IFMT {
	case syscall.S_IFBLK:
		mode |= os.ModeDevice
	case syscall.S_IFCHR:
		mode |= os.ModeDevice | os.ModeCharDevice
	case syscall.S_IFDIR:
		mode |= os.ModeDir
	case syscall.S_IFIFO:
		mode |= os.ModeNamedPipe
	case syscall.S_IFLNK:
		mode |= os.ModeSymlink
	case syscall.S_IFSOCK:
		mode |= os.ModeSocket
	}
	if stat.Mode&syscall.S_ISGID != 0 {
		mode |= os.ModeSetgid
	}
	if stat.Mode&syscall.S_ISUID != 0 {
		mode |= os.ModeSetuid
	}
	if stat.Mode&syscall.S_ISVTX != 0 {
		mode |= os.ModeSticky
	}
	attr.Inode = stat.Ino
	attr.Nlink = uint32(stat.Nlink)
	attr.Mode = mode
	attr.Uid = uint32(stat.Uid)
	attr.Gid = stat.Gid
	attr.Rdev = uint32(stat.Rdev)
	attr.Size = uint64(stat.Size)
	attr.BlockSize = uint32(stat.Blksize)
	attr.Blocks = uint64(stat.Blocks)
	attr.Atime = convertTime(stat.Atim)
	attr.Mtime = convertTime(stat.Mtim)
	attr.Ctime = convertTime(stat.Ctim)
}

func (d *directory) Attr(_ context.Context, attr *fuse.Attr) error {
	stat := &syscall.Stat_t{}
	if err := syscall.Stat(d.FullPath(), stat); err != nil {
		return osToFuseErr(err)
	}
	copyStatToAttr(stat, attr)
	return nil
}

func (d *directory) ReadDirAll(context.Context) ([]fuse.Dirent, error) {
	files, err := ioutil.ReadDir(d.FullPath())
	if err != nil {
		return nil, osToFuseErr(err)
	}
	entries := make([]fuse.Dirent, len(files))
	for i, f := range files {
		var inode uint64
		if sys := f.Sys(); sys != nil {
			inode = sys.(*syscall.Stat_t).Ino
		}
		direntType := fuse.DT_Unknown
		mode := f.Mode()
		if mode.IsRegular() {
			direntType = fuse.DT_File
		} else if mode.IsDir() {
			direntType = fuse.DT_Dir
		} else if mode&os.ModeSymlink != 0 {
			direntType = fuse.DT_Link
		} else if mode&os.ModeSocket != 0 {
			direntType = fuse.DT_Socket
		} else if mode&os.ModeDevice != 0 {
			direntType = fuse.DT_Block
		} else if mode&os.ModeCharDevice != 0 {
			direntType = fuse.DT_Char
		} else if mode&os.ModeNamedPipe != 0 {
			direntType = fuse.DT_FIFO
		}
		entries[i] = fuse.Dirent{
			Inode: inode,
			Type:  direntType,
			Name:  f.Name(),
		}
	}
	return entries, nil
}

func (d *directory) Lookup(_ context.Context, name string) (fs.Node, error) {
	rootRelativePath := path.Join(d.rootRelativePath, name)
	fullPath := path.Join(d.FullPath(), name)
	stat, err := os.Stat(fullPath)
	if err != nil {
		return nil, osToFuseErr(err)
	}
	newNode := &node{d.splitFS, rootRelativePath}
	mode := stat.Mode()
	if mode.IsDir() {
		return &directory{newNode}, nil
	}
	if mode.IsRegular() {
		fileHash := fnv.New64()
		fileHash.Sum([]byte(rootRelativePath))
		return &fileAsDir{newNode, fileHash.Sum64()}, nil
	}
	// TODO: Implement other types.
	return nil, errors.New("unimplemented")
}

type fileAsDir struct {
	*node
	hash uint64
}

var _ fs.Node = (*fileAsDir)(nil)
var _ fs.Handle = (*fileAsDir)(nil)
var _ fs.HandleReadDirAller = (*fileAsDir)(nil)

func (f *fileAsDir) Attr(_ context.Context, attr *fuse.Attr) error {
	stat := &syscall.Stat_t{}
	if err := syscall.Stat(f.FullPath(), stat); err != nil {
		return osToFuseErr(err)
	}
	copyStatToAttr(stat, attr)
	attr.Mode = (attr.Mode & 0555) | os.ModeDir
	return nil
}

const minFormatZeroes = 8

var fileAsDirFormatString = fmt.Sprintf("%%x_%%0%dd_of_%%0%dd", minFormatZeroes, minFormatZeroes)

func (f *fileAsDir) ReadDirAll(context.Context) ([]fuse.Dirent, error) {
	stat, err := os.Stat(f.FullPath())
	if err != nil {
		return nil, osToFuseErr(err)
	}
	size := big.NewInt(stat.Size())
	quotient := big.NewInt(0)
	remainder := big.NewInt(0)
	quotient, remainder = quotient.DivMod(size, f.splitFS.chunkSize, remainder)
	numChunks := quotient.Uint64()
	if remainder.Uint64() > 0 {
		numChunks++
	}
	entries := make([]fuse.Dirent, numChunks)
	lastChunkNumber := numChunks - 1
	for i := uint64(0); i < numChunks; i++ {
		entries[i] = fuse.Dirent{
			Inode: f.hash + i,
			Type:  fuse.DT_File,
			Name:  fmt.Sprintf(fileAsDirFormatString, f.hash, i, lastChunkNumber),
		}
	}
	return entries, nil
}

func (f *fileAsDir) Lookup(_ context.Context, name string) (fs.Node, error) {
	return nil, errors.New("unimplemented")
}
