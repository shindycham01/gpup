package photos

import (
	"fmt"
	"io/ioutil"
	"net/http"
	"os"
	"path"
	"sync"

	photoslibrary "google.golang.org/api/photoslibrary/v1"
)

const uploadConcurrency = 3
const apiVersion = "v1"
const basePath = "https://photoslibrary.googleapis.com/"

// UploadFiles uploads the files.
// This method tries uploading all files and ignores any error.
// If no file could be uploaded, this method returns an empty array.
func (p *Photos) UploadFiles(filepaths []string) []*photoslibrary.NewMediaItem {
	uploadQueue := make(chan string, len(filepaths))
	for _, filepath := range filepaths {
		uploadQueue <- filepath
	}
	close(uploadQueue)
	p.log.Printf("Queued %d file(s)", len(filepaths))

	aggregateQueue := make(chan *photoslibrary.NewMediaItem, len(filepaths))
	workerGroup := new(sync.WaitGroup)
	for i := 0; i < uploadConcurrency; i++ {
		workerGroup.Add(1)
		go p.uploadWorker(uploadQueue, aggregateQueue, workerGroup)
	}
	go func() {
		workerGroup.Wait()
		close(aggregateQueue)
	}()

	mediaItems := make([]*photoslibrary.NewMediaItem, 0, len(filepaths))
	for mediaItem := range aggregateQueue {
		mediaItems = append(mediaItems, mediaItem)
	}
	return mediaItems
}

func (p *Photos) uploadWorker(uploadQueue chan string, aggregateQueue chan *photoslibrary.NewMediaItem, workerGroup *sync.WaitGroup) {
	defer workerGroup.Done()
	for filepath := range uploadQueue {
		mediaItem, err := p.UploadFile(filepath)
		if err != nil {
			p.log.Printf("Error while uploading file %s: %s", filepath, err)
		} else {
			aggregateQueue <- mediaItem
		}
	}
}

// UploadFile uploads the file.
func (p *Photos) UploadFile(filepath string) (*photoslibrary.NewMediaItem, error) {
	r, err := os.Open(filepath)
	if err != nil {
		return nil, fmt.Errorf("Could not open file %s: %s", filepath, err)
	}
	defer r.Close()

	filename := path.Base(filepath)
	req, err := http.NewRequest("POST", fmt.Sprintf("%s%s/uploads", basePath, apiVersion), r)
	if err != nil {
		return nil, fmt.Errorf("Could not create a request for uploading file %s: %s", filepath, err)
	}
	req.Header.Add("X-Goog-Upload-File-Name", filename)

	p.log.Printf("Uploading %s", filepath)
	res, err := p.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("Could not send a request for uploading file %s: %s", filepath, err)
	}
	defer res.Body.Close()

	b, err := ioutil.ReadAll(res.Body)
	if err != nil {
		return nil, fmt.Errorf("Could not read the response body while uploading file %s: status=%d, %s", filepath, res.StatusCode, err)
	}
	body := string(b)

	if res.StatusCode != 200 {
		return nil, fmt.Errorf("Could not upload file %s: status=%d, body=%s", filepath, res.StatusCode, body)
	}
	return &photoslibrary.NewMediaItem{
		Description: filename,
		SimpleMediaItem: &photoslibrary.SimpleMediaItem{
			UploadToken: body,
		},
	}, nil
}

// Append appends the items to the album or your library (if album is nil).
// If some item(s) have been failed, this method does not return an error but prints message(s).
// If a network error occurs, this method returns the error.
func (p *Photos) Append(album *photoslibrary.Album, mediaItems []*photoslibrary.NewMediaItem) error {
	req := &photoslibrary.BatchCreateMediaItemsRequest{
		NewMediaItems: mediaItems,
	}
	if album != nil {
		req.AlbumId = album.Id
	}
	batch, err := p.service.MediaItems.BatchCreate(req).Do()
	if err != nil {
		return err
	}
	for _, result := range batch.NewMediaItemResults {
		if result.Status.Code != 0 {
			if mediaItem := findMediaItemByUploadToken(mediaItems, result.UploadToken); mediaItem != nil {
				p.log.Printf("Skipped %s: %s (%d)", mediaItem.Description, result.Status.Message, result.Status.Code)
			} else {
				p.log.Printf("Error while adding files: %s (%d)", result.Status.Message, result.Status.Code)
			}
		}
	}
	return nil
}

func findMediaItemByUploadToken(mediaItems []*photoslibrary.NewMediaItem, uploadToken string) *photoslibrary.NewMediaItem {
	for _, mediaItem := range mediaItems {
		if mediaItem.SimpleMediaItem.UploadToken == uploadToken {
			return mediaItem
		}
	}
	return nil
}