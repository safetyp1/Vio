package downloads

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/downloadprepare"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/playback"
)

type virtualDownloadMetadataResolver interface {
	ResolveVirtualDownloadMetadata(context.Context, *models.MediaFile) (*models.MediaFile, error)
}

func (s *Service) resolveDownloadMetadata(ctx context.Context, file *models.MediaFile, caps playback.ClientCapabilities) (*models.MediaFile, error) {
	if !isVirtualMediaFile(file) || !hasCapabilities(caps) || (file.Container != "" && !strings.EqualFold(file.Container, "virtual")) {
		return file, nil
	}
	resolver, ok := s.virtualInput.(virtualDownloadMetadataResolver)
	if !ok {
		return file, nil
	}
	resolved, err := resolver.ResolveVirtualDownloadMetadata(ctx, file)
	if err != nil || resolved == nil || resolved.ID != file.ID || resolved.FilePath != file.FilePath {
		return nil, ErrQualityUnavailable
	}
	return resolved, nil
}

var virtualDownloadClient = func() *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.DisableCompression = true
	transport.ResponseHeaderTimeout = 30 * time.Second
	return &http.Client{
		Transport: transport,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}()

func (s *Service) SetVirtualInputResolver(resolver VirtualInputResolver) {
	s.virtualInput = resolver
}

func (s *Service) serveVirtualOriginal(ctx context.Context, w http.ResponseWriter, r *http.Request, file *models.MediaFile, userID int) error {
	if s.virtualInput == nil {
		return ErrFormatUnavailable
	}
	input, cleanup, err := s.virtualInput.ResolveVirtualDownloadInput(ctx, file)
	if cleanup != nil {
		defer cleanup()
	}
	if err != nil {
		return errors.New("virtual download source is unavailable")
	}
	if !isLoopbackPrepareInput(input) {
		return errors.New("virtual download source requires a local relay")
	}
	request, err := http.NewRequestWithContext(ctx, r.Method, input, nil)
	if err != nil {
		return errors.New("invalid virtual download relay request")
	}
	for _, name := range []string{"Range", "If-Range", "If-Match", "If-None-Match", "If-Modified-Since", "If-Unmodified-Since"} {
		if value := r.Header.Get(name); value != "" {
			request.Header.Set(name, value)
		}
	}
	request.Header.Set("Accept-Encoding", "identity")
	response, err := virtualDownloadClient.Do(request)
	if err != nil {
		return errors.New("virtual download relay is unavailable")
	}
	defer func() { _ = response.Body.Close() }()
	if !downloadprepare.RelayStatusAllowed(response.StatusCode) {
		return fmt.Errorf("virtual download relay returned %d", response.StatusCode)
	}
	contentType := strings.ToLower(response.Header.Get("Content-Type"))
	if strings.Contains(contentType, "mpegurl") || strings.Contains(contentType, "dash+xml") {
		return ErrFormatUnavailable
	}
	w.Header().Set("Content-Disposition", attachmentDisposition(fmt.Sprintf("download-%d", file.ID)))
	downloadprepare.CopyResponseHeaders(w.Header(), response.Header)
	if response.StatusCode == http.StatusPreconditionFailed || response.StatusCode == http.StatusRequestedRangeNotSatisfiable {
		w.Header().Set("Content-Length", "0")
		w.WriteHeader(response.StatusCode)
		return nil
	}
	w.WriteHeader(response.StatusCode)
	if r.Method == http.MethodHead || response.StatusCode == http.StatusNotModified {
		return nil
	}
	var reader io.Reader = response.Body
	if s.bandwidth != nil {
		reader = s.bandwidth.ThrottledStreamReader(ctx, reader, userID)
	}
	written, err := io.Copy(w, reader)
	if err != nil || (response.ContentLength >= 0 && written != response.ContentLength) {
		return fmt.Errorf("%w: virtual download transfer interrupted", ErrResponseCommitted)
	}
	return nil
}
