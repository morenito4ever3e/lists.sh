package scp

import (
	"fmt"
	"io"
	"log"
	"time"

	"github.com/gliderlabs/ssh"
	"github.com/neurosnap/lists.sh/internal"
	"github.com/neurosnap/lists.sh/internal/db"
	"github.com/neurosnap/lists.sh/pkg"
)

type Opener struct {
	entry *FileEntry
}

func (o *Opener) Open(name string) (io.Reader, error) {
	return o.entry.Reader, nil
}

type DbHandler struct{}

func (h *DbHandler) Write(s ssh.Session, entry *FileEntry, user *db.User, dbpool db.DB) error {
	userID := user.ID
	filename := internal.SanitizeFileExt(entry.Name)
	title := filename
	post, err := dbpool.FindPostWithFilename(filename, userID)

	var text string
	if b, err := io.ReadAll(entry.Reader); err == nil {
		text = string(b)
	}

	if !internal.IsTextFile(text, entry.Filepath) {
		return fmt.Errorf("file must be a text file")
	}

	parsedText := pkg.ParseText(text)
	if parsedText.MetaData.Title != "" {
		title = parsedText.MetaData.Title
	}
	description := parsedText.MetaData.Description

	if post == nil {
		publishAt := time.Now()
		if parsedText.MetaData.PublishAt != nil {
			publishAt = *parsedText.MetaData.PublishAt
		}
		log.Printf("%s not found, adding record", title)
		post, err = dbpool.InsertPost(userID, filename, title, text, description, &publishAt)
		if err != nil {
			return fmt.Errorf("error for %s: %v", title, err)
		}
	} else {
		publishAt := post.PublishAt
		fmt.Println(parsedText.MetaData.PublishAt)
		if parsedText.MetaData.PublishAt != nil {
			publishAt = parsedText.MetaData.PublishAt
		}
		log.Printf("%s found, updating record", title)
		post, err = dbpool.UpdatePost(post.ID, title, text, description, publishAt)
		if err != nil {
			return fmt.Errorf("error for %s: %v", title, err)
		}
	}

	return nil
}
