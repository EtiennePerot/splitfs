package split

import (
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"io/ioutil"
	"math/big"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"bazil.org/fuse"
	"bazil.org/fuse/fs"
	"golang.org/x/net/context"
)

type splitFS struct {
	sourceDirectory string
	chunkSize       int64
	excludeRegexp   *regexp.Regexp
}

var _ fs.FS = (*splitFS)(nil)

type Option func(*splitFS) error

func ExcludeRegexp(exclude string) Option {
	return func(f *splitFS) error {
		excludeRegexp, err := regexp.Compile(exclude)
		if err != nil {
			return fmt.Errorf("invalid regexp %q: %v", exclude, err)
		}
		f.excludeRegexp = excludeRegexp
		return nil
	}
}

func (f *splitFS) Root() (fs.Node, error) {
	return &directory{&node{f, ""}}, nil
}

func (f *splitFS) IsExcluded(path string) bool {
	if f.excludeRegexp == nil {
		return false
	}
	return f.excludeRegexp.MatchString(path)
}

func NewFS(sourceDirectory string, chunkSize int64, options ...Option) (fs.FS, error) {
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
	f := &splitFS{
		sourceDirectory: absoluteSource,
		chunkSize:       chunkSize,
	}
	for _, option := range options {
		if err := option(f); err != nil {
			return nil, fmt.Errorf("canot apply options: %v", err)
		}
	}
	return f, nil
}

type node struct {
	splitFS          *splitFS
	rootRelativePath string
}

func (n *node) FullPath() string {
	return path.Join(n.splitFS.sourceDirectory, n.rootRelativePath)
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

func convertTime(timespec syscall.Timespec) time.Time {
	sec, nsec := timespec.Unix()
	return time.Unix(sec, nsec)
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

func (n *node) Attr(_ context.Context, attr *fuse.Attr) error {
	stat := &syscall.Stat_t{}
	if err := syscall.Lstat(n.FullPath(), stat); err != nil {
		return osToFuseErr(err)
	}
	copyStatToAttr(stat, attr)
	return nil
}

type directory struct {
	*node
}

var _ fs.Node = (*directory)(nil)
var _ fs.HandleReadDirAller = (*directory)(nil)

func (d *directory) ReadDirAll(context.Context) ([]fuse.Dirent, error) {
	fullPath := d.FullPath()
	files, err := ioutil.ReadDir(fullPath)
	if err != nil {
		return nil, osToFuseErr(err)
	}
	entries := make([]fuse.Dirent, len(files))
	for i, f := range files {
		name := f.Name()
		isExcluded := d.splitFS.IsExcluded(path.Join(fullPath, name))
		var inode uint64
		if sys := f.Sys(); sys != nil {
			inode = sys.(*syscall.Stat_t).Ino
		}
		direntType := fuse.DT_Unknown
		mode := f.Mode()
		if mode.IsRegular() {
			if isExcluded {
				direntType = fuse.DT_File
			} else {
				direntType = fuse.DT_Dir
			}
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
			Name:  name,
		}
	}
	return entries, nil
}

func (d *directory) Lookup(_ context.Context, name string) (fs.Node, error) {
	rootRelativePath := path.Join(d.rootRelativePath, name)
	fullPath := path.Join(d.FullPath(), name)
	stat, err := os.Lstat(fullPath)
	if err != nil {
		return nil, osToFuseErr(err)
	}
	newNode := &node{d.splitFS, rootRelativePath}
	mode := stat.Mode()
	if mode.IsDir() {
		return &directory{newNode}, nil
	}
	if mode.IsRegular() {
		if d.splitFS.IsExcluded(fullPath) {
			return &directFile{newNode}, nil
		}
		fileHash := fnv.New64a()
		rootRelativePathBytes := []byte(rootRelativePath)
		written, err := fileHash.Write(rootRelativePathBytes)
		if err != nil {
			return nil, fmt.Errorf("cannot compute hash: %v", err)
		}
		if written != len(rootRelativePathBytes) {
			return nil, fmt.Errorf("could not write all bytes to file hash: %d bytes written, but expected %d bytes", written, len(rootRelativePathBytes))
		}
		return &fileAsDir{newNode, fileHash.Sum64()}, nil
	}
	if mode&os.ModeSymlink != 0 {
		return &symlink{newNode}, nil
	}
	// TODO: Implement other types.
	return nil, errors.New("unimplemented")
}

type directFile struct {
	*node
}

var _ fs.Node = (*directFile)(nil)
var _ fs.NodeOpener = (*directFile)(nil)

var handleIDProvider <-chan fuse.HandleID

func init() {
	idProvider := make(chan fuse.HandleID)
	handleIDProvider = idProvider
	go func() {
		for id := fuse.HandleID(2); ; id++ {
			idProvider <- id
		}
	}()
}

func (f *directFile) Open(_ context.Context, req *fuse.OpenRequest, resp *fuse.OpenResponse) (fs.Handle, error) {
	if !req.Flags.IsReadOnly() {
		return nil, fuse.Errno(syscall.EROFS)
	}
	file, err := os.Open(f.FullPath())
	if err != nil {
		return nil, osToFuseErr(err)
	}
	resp.Handle = <-handleIDProvider
	return &directFileHandle{f, file}, nil
}

type directFileHandle struct {
	*directFile
	file *os.File
}

var _ fs.Handle = (*directFileHandle)(nil)
var _ fs.HandleReader = (*directFileHandle)(nil)
var _ fs.HandleReleaser = (*directFileHandle)(nil)

func (f *directFileHandle) Read(_ context.Context, req *fuse.ReadRequest, resp *fuse.ReadResponse) error {
	bytes := make([]byte, req.Size)
	read, err := f.file.ReadAt(bytes, req.Offset)
	if err != nil && err != io.EOF {
		return osToFuseErr(err)
	}
	resp.Data = bytes[:read]
	return nil
}

func (f *directFileHandle) Release(_ context.Context, req *fuse.ReleaseRequest) error {
	if err := f.file.Close(); err != nil {
		return osToFuseErr(err)
	}
	return nil
}

type symlink struct {
	*node
}

var _ fs.Node = (*symlink)(nil)
var _ fs.NodeReadlinker = (*symlink)(nil)

func (s *symlink) Readlink(context.Context, *fuse.ReadlinkRequest) (string, error) {
	link, err := os.Readlink(s.FullPath())
	if err != nil {
		return "", osToFuseErr(err)
	}
	return link, nil
}

type fileAsDir struct {
	*node
	hash uint64
}

var _ fs.Node = (*fileAsDir)(nil)
var _ fs.HandleReadDirAller = (*fileAsDir)(nil)

func (f *fileAsDir) Attr(ctx context.Context, attr *fuse.Attr) error {
	if err := f.node.Attr(ctx, attr); err != nil {
		return err
	}
	attr.Mode = (attr.Mode & 0555) | os.ModeDir
	return nil
}

const minFormatZeroes = 8
const chunkFileExtension = ".splitfs.chunk"

var fileAsDirFormatString = fmt.Sprintf("%%x_%%0%dd_of_%%0%dd%s", minFormatZeroes, minFormatZeroes, chunkFileExtension)

// ceilAndRemainder returns (ceil(x / y), x mod y).
// It panics if y == 0.
func ceilAndRemainder(x, y int64) (int64, int64) {
	bigQ, bigR := big.NewInt(0).DivMod(big.NewInt(x), big.NewInt(y), big.NewInt(0))
	q, r := bigQ.Int64(), bigR.Int64()
	if r > 0 {
		q++
	}
	return q, r
}

// getChunks returns (number of chunks, size of last chunk in bytes, error)
func (f *fileAsDir) getChunks() (int64, int64, error) {
	stat, err := os.Stat(f.FullPath())
	if err != nil {
		return 0, 0, err
	}
	numChunks, lastChunkSize := ceilAndRemainder(stat.Size(), f.splitFS.chunkSize)
	return numChunks, lastChunkSize, nil
}

func (f *fileAsDir) ReadDirAll(context.Context) ([]fuse.Dirent, error) {
	numChunks, _, err := f.getChunks()
	if err != nil {
		return nil, osToFuseErr(err)
	}
	entries := make([]fuse.Dirent, numChunks)
	for i := int64(0); i < numChunks; i++ {
		entries[i] = fuse.Dirent{
			Inode: f.hash + uint64(i+1),
			Type:  fuse.DT_File,
			Name:  fmt.Sprintf(fileAsDirFormatString, f.hash, i+1, numChunks),
		}
	}
	return entries, nil
}

func (f *fileAsDir) Lookup(_ context.Context, name string) (fs.Node, error) {
	if !strings.HasSuffix(name, chunkFileExtension) {
		return nil, fuse.ENOENT
	}
	parts := strings.Split(strings.TrimSuffix(name, chunkFileExtension), "_")
	if len(parts) != 4 {
		return nil, fuse.ENOENT
	}
	hashPart, chunkPart, ofPart, totalChunksPart := parts[0], parts[1], parts[2], parts[3]
	if ofPart != "of" {
		return nil, fuse.ENOENT
	}
	hash, err := strconv.ParseUint(hashPart, 16, 64)
	if err != nil || hash != f.hash {
		return nil, fuse.ENOENT
	}
	chunk, err := strconv.ParseInt(chunkPart, 10, 64)
	if err != nil || chunk < 0 {
		return nil, fuse.ENOENT
	}
	chunk-- // Filenames are 1-indexed, so convert back down to 0.
	numChunksFromFilename, err := strconv.ParseInt(totalChunksPart, 10, 64)
	if err != nil {
		return nil, fuse.ENOENT
	}
	numChunks, lastChunkSize, err := f.getChunks()
	if err != nil {
		return nil, osToFuseErr(err)
	}
	if numChunksFromFilename != numChunks || chunk >= numChunks {
		return nil, fuse.ENOENT
	}
	size := f.splitFS.chunkSize
	if chunk == numChunks-1 {
		size = lastChunkSize
	}
	return &fileChunk{
		node:   f.node,
		chunk:  chunk,
		offset: chunk * f.splitFS.chunkSize,
		size:   size,
	}, nil
}

type fileChunk struct {
	*node
	chunk  int64
	offset int64
	size   int64
}

var _ fs.Node = (*fileChunk)(nil)
var _ fs.NodeOpener = (*fileChunk)(nil)

func (f *fileChunk) Attr(ctx context.Context, attr *fuse.Attr) error {
	if err := f.node.Attr(ctx, attr); err != nil {
		return err
	}
	attr.Inode += uint64(f.chunk + 1)
	attr.Size = uint64(f.size)
	numBlocks, _ := ceilAndRemainder(f.size, 512)
	attr.Blocks = uint64(numBlocks)
	return nil
}

func (f *fileChunk) Open(_ context.Context, req *fuse.OpenRequest, resp *fuse.OpenResponse) (fs.Handle, error) {
	if !req.Flags.IsReadOnly() {
		return nil, fuse.Errno(syscall.EROFS)
	}
	file, err := os.Open(f.FullPath())
	if err != nil {
		return nil, osToFuseErr(err)
	}
	if f.offset != 0 {
		if _, err := file.Seek(f.offset, 0); err != nil {
			return nil, osToFuseErr(err)
		}
	}
	resp.Handle = <-handleIDProvider
	return &fileChunkHandle{f, file}, nil
}

type fileChunkHandle struct {
	*fileChunk
	file *os.File
}

var _ fs.Handle = (*fileChunkHandle)(nil)
var _ fs.HandleReader = (*fileChunkHandle)(nil)
var _ fs.HandleReleaser = (*fileChunkHandle)(nil)

func (f *fileChunkHandle) Read(_ context.Context, req *fuse.ReadRequest, resp *fuse.ReadResponse) error {
	trueOffset := req.Offset + f.offset
	trueSize := int64(req.Size)
	if trueSize > f.size-req.Offset {
		trueSize = f.size - req.Offset
	}
	if trueSize < 0 {
		trueSize = 0
	}
	bytes := make([]byte, trueSize)
	read, err := f.file.ReadAt(bytes, trueOffset)
	if err != nil && err != io.EOF {
		return osToFuseErr(err)
	}
	resp.Data = bytes[:read]
	return nil
}

func (f *fileChunkHandle) Release(_ context.Context, req *fuse.ReleaseRequest) error {
	if err := f.file.Close(); err != nil {
		return osToFuseErr(err)
	}
	return nil
}
