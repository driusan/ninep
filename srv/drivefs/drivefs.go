// Copyright 2009 The Ninep Authors.  All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package drivefs

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"path"

	"github.com/lionkov/ninep"
	"github.com/lionkov/ninep/srv"
	"golang.org/x/net/context"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	"google.golang.org/api/drive/v3"
)

const (
	Qroot = 'r'
)

type DriveID string
type QIDString string

type DriveFS struct {
	srv.Srv
	Listener net.Listener
	Secret   string
	Cache    string
	Context  context.Context
	Config   *oauth2.Config
	Drive    *drive.Service
	//NFS *drive.FileService
	Token  oauth2.Token
	QidMap map[uint64]*Fid
	FidMap map[DriveID]*Fid
}

type config func(*DriveFS) error

type DriveFile struct {
	Name   string
	ID     DriveID
	Parent *ninep.Qid
	QID    *ninep.Qid
}

type Fid struct {
	DriveFile
}

var (
	// preallocate QIDS
	dirQids = map[string]*ninep.Qid{
		"/": &ninep.Qid{Type: ninep.QTDIR, Version: 0777, Path: Qroot},
		".": &ninep.Qid{Type: ninep.QTDIR, Version: 0777, Path: 0},
	}

	// Start the drive QIDs of at an insane number. Simple reason: catch bugs that we might
	// not see until we hit 2^32 files. Ha ha.
	nextQID = uint64(1 << 32)
	// Verify that we correctly implement ReqOps
	_ = srv.ReqOps(&DriveFS{})
)

func (f *DriveFS) Read(r *srv.Req) {
	r.RespondError(&ninep.Error{Err: "NOT YET", Errornum: ninep.EINVAL})
}

func (*DriveFS) Clunk(r *srv.Req) {
	r.RespondError(&ninep.Error{Err: "NOT YET", Errornum: ninep.EINVAL})
}

func (f *DriveFS) Write(r *srv.Req) {
	r.RespondError(&ninep.Error{Err: "NOT YET", Errornum: ninep.EINVAL})
}

// Walk walks from a drive file to another file.
func (f *DriveFS) Walk(r *srv.Req) {
	tc := r.Tc
	fid := r.Fid.Aux.(*Fid)
	log.Printf("Walk: fid %v", fid)

	if len(tc.Wname) > 1 && tc.Qid.Type != ninep.QTDIR {
		r.RespondError(ninep.ENOENT)
		return
	}

	// The most common case is walking from '.', so we initialize to '.' and fix it up
	// later if needed.
	if r.Newfid.Aux == nil {
		r.Newfid.Aux = &Fid{DriveFile: DriveFile{Name: ".", QID: dirQids["."]}}
	}

	if len(tc.Wname) == 0 {
		r.RespondRwalk([]ninep.Qid{})
		return
	}

	nfid := r.Newfid.Aux.(*Fid)
	p := path.Join(fid.Name, path.Join(tc.Wname...))
	log.Printf("Walk to %v", p)
	// The docs say title, but what works is using name.
	p = "trashed = false and name = '" + p + "'"
	log.Printf("Walk to %v", p)
	files := f.Drive.Files.List().Q(p).
		Fields("files(id, name)")

	rr, err := files.Do()
	if err != nil || len(rr.Files) == 0 {
		r.RespondError(&ninep.Error{Err: fmt.Sprintf("%v", err), Errornum: ninep.ENOENT})
		return
	}

	if len(rr.Files) > 1 {
		log.Printf("non unique name, what to do?")
		r.RespondError(&ninep.Error{Err: "Non unique name", Errornum: ninep.ENOENT})
		return
	}

	i := rr.Files[0]

	if nf, ok := f.FidMap[DriveID(i.Id)]; !ok {
		qid := nextQID
		nextQID++
		nfid.Name = i.Name
		nfid.ID = DriveID(i.Id)
		nfid.QID = &ninep.Qid{Path: qid, Version: 0555}
		f.FidMap[DriveID(i.Id)] = nfid
		f.QidMap[qid] = nfid
	} else {
		nfid = nf
	}

	r.Newfid.Aux.(*Fid).Name = i.Name
	qids := make([]ninep.Qid, len(tc.Wname)-1)
	r.Newfid.Aux.(*Fid).QID = nfid.QID
	qids = append(qids, *nfid.QID)
	log.Printf("Return fro walk is %v", qids)
	r.RespondRwalk(qids)

}

func (f *DriveFS) Create(r *srv.Req) {
	r.RespondError(&ninep.Error{Err: "NOT YET", Errornum: ninep.EINVAL})
}

func (f *DriveFS) Open(r *srv.Req) {
	r.RespondError(&ninep.Error{Err: "NOT YET", Errornum: ninep.EINVAL})
}

func (f *DriveFS) Remove(r *srv.Req) {
	r.RespondError(&ninep.Error{Err: "NOT YET", Errornum: ninep.EINVAL})
}

func (f *DriveFS) Stat(r *srv.Req) {
	r.RespondError(&ninep.Error{Err: "NOT YET", Errornum: ninep.EINVAL})
}

func (f *DriveFS) Wstat(r *srv.Req) {
	r.RespondError(&ninep.Error{Err: "NOT YET", Errornum: ninep.EINVAL})
}

// Attach responds to an attach message from the 9p client. It results in create a connection
// to drive.
func (fs *DriveFS) Attach(r *srv.Req) {
	if r.Afid != nil {
		r.RespondError(srv.Enoauth)
		return
	}

	ctx := context.Background()

	// We thought about putting the secret and cache info in the Aname. We might yet do it.
	// Tough call, but realistically, the user is starting the server, so we're going to let them
	// set up the server secret and cache when they start the server. This may change.
	// We don't print the Secret or the Cache when this fails for what I think are obvious reasons.
	config, err := google.ConfigFromJSON([]byte(fs.Secret), drive.DriveMetadataReadonlyScope)
	if err != nil {
		log.Fatalf("Unable to parse client secret file to config: %v", err)
	}

	if err := json.NewDecoder(bytes.NewReader([]byte(fs.Cache))).Decode(&fs.Token); err != nil {
		log.Fatalf("Unable to get a token from the string: fs.Cache %v", err)
	}
	client := config.Client(ctx, &fs.Token)

	drive, err := drive.New(client)
	if err != nil {
		log.Fatalf("Unable to retrieve drive Client %v", err)
	}

	fid := &Fid{DriveFile: DriveFile{Name: ".", QID: dirQids["/"]}}
	r.Fid.Aux = fid

	fs.Config, fs.Context, fs.Drive = config, ctx, drive
	fs.QidMap = make(map[uint64]*Fid)
	fs.FidMap = make(map[DriveID]*Fid)
	fs.QidMap[Qroot] = fid
	// And what's the ID for the mount point? we don't know yet.

	r.RespondRattach(fid.QID)
}

func NewDriveFS(c ...config) (*DriveFS, error) {
	f := new(DriveFS)

	if !f.Start(f) {
		return nil, fmt.Errorf("Can't happen: Starting the server failed")
	}

	for i := range c {
		if err := c[i](f); err != nil {
			return nil, err
		}
	}
	//f.NFS = drive.NewFileService(f.Service)
	return f, nil
}