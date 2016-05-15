package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"

	"bazil.org/fuse"
	"bazil.org/fuse/fs"
	"perot.me/splitfs/split"
)

var progName = filepath.Base(os.Args[0])

func usage() {
	fmt.Fprintf(os.Stderr, "Usage of %s:\n", progName)
	fmt.Fprintf(os.Stderr, "  %s <chunk size in bytes> <source directory> <target mountpoint>\n", progName)
	flag.PrintDefaults()
}

func main() {
	log.SetFlags(0)
	log.SetPrefix(fmt.Sprintf("%s: ", progName))
	flag.Usage = usage
	flag.Parse()
	if flag.NArg() != 3 {
		usage()
		os.Exit(2)
	}
	chunkSize, err := strconv.Atoi(flag.Arg(0))
	if err != nil {
		log.Fatalf("Invalid chunk size: %v (must be a number of bytes): %v", chunkSize, err)
	}
	sourceDirectory := flag.Arg(1)
	targetMountpoint := flag.Arg(2)
	splitFS, err := split.NewFS(sourceDirectory, int64(chunkSize))
	if err != nil {
		log.Fatalf("Cannot initialize filesystem: %v", err)
	}
	fuseConn, err := fuse.Mount(
		targetMountpoint,
		fuse.FSName("splitfs"),
		fuse.LocalVolume(),
		fuse.VolumeName(fmt.Sprintf("splitfs %d %s", chunkSize, filepath.Base(sourceDirectory))))
	if err != nil {
		log.Fatalf("Cannot mount a filesystem at %q: %v", targetMountpoint, err)
	}
	defer fuseConn.Close()
	if err = fs.Serve(fuseConn, splitFS); err != nil {
		log.Fatalf("Cannot serve filesystem: %v", err)
	}
	<-fuseConn.Ready
	if err := fuseConn.MountError; err != nil {
		log.Fatal("Mount error: %v", err)
	}
}
