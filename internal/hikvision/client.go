package hikvision

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/RickDB/HikFootageServer/internal/store"
)

type DeviceInfo struct {
	DeviceName       string `xml:"deviceName"`
	DeviceID         string `xml:"deviceID"`
	Model            string `xml:"model"`
	Serial           string `xml:"serialNumber"`
	Firmware         string `xml:"firmwareVersion"`
	FirmwareReleased string `xml:"firmwareReleasedDate"`
	DeviceType       string `xml:"deviceType"`
}
type Capabilities struct {
	ISAPI, DeviceInfo, ChannelDiscovery, RecordingSearch, DirectDownload, HTTPS, DigestAuth bool
	DownloadModes                                                                           []string
	Notes                                                                                   []string
}
type Client struct {
	base               *url.URL
	username, password string
	http               *http.Client
}

func New(n store.NVR, timeout time.Duration) (*Client, error) {
	u, err := url.Parse(fmt.Sprintf("%s://%s:%d", n.Scheme, n.Host, n.Port))
	if err != nil {
		return nil, err
	}
	tr := &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: !n.VerifyTLS}, MaxIdleConnsPerHost: 2, IdleConnTimeout: 30 * time.Second}
	return &Client{base: u, username: n.Username, password: n.Password, http: &http.Client{Transport: tr, Timeout: timeout}}, nil
}

func (c *Client) Device(ctx context.Context) (DeviceInfo, error) {
	var v DeviceInfo
	r, e := c.request(ctx, http.MethodGet, "/ISAPI/System/deviceInfo", nil, "application/xml")
	if e != nil {
		return v, e
	}
	defer r.Body.Close()
	if e = xml.NewDecoder(r.Body).Decode(&v); e != nil {
		return v, fmt.Errorf("decode deviceInfo: %w", e)
	}
	return v, nil
}

type inputProxyList struct {
	Items []struct {
		ID                        int    `xml:"id"`
		Name                      string `xml:"name"`
		SourceInputPortDescriptor struct {
			ProxyProtocol        string `xml:"proxyProtocol"`
			AddressingFormatType string `xml:"addressingFormatType"`
		} `xml:"sourceInputPortDescriptor"`
	} `xml:"InputProxyChannel"`
}
type streamingList struct {
	Items []struct {
		ID      string `xml:"id"`
		Name    string `xml:"channelName"`
		Enabled bool   `xml:"enabled"`
	} `xml:"StreamingChannel"`
}

func (c *Client) Channels(ctx context.Context) ([]store.Camera, error) {
	r, e := c.request(ctx, http.MethodGet, "/ISAPI/ContentMgmt/InputProxy/channels", nil, "application/xml")
	if e == nil {
		defer r.Body.Close()
		var v inputProxyList
		if xml.NewDecoder(r.Body).Decode(&v) == nil && len(v.Items) > 0 {
			out := make([]store.Camera, 0, len(v.Items))
			for _, x := range v.Items {
				name := x.Name
				if name == "" {
					name = fmt.Sprintf("Camera %d", x.ID)
				}
				out = append(out, store.Camera{ChannelID: strconv.Itoa(x.ID*100 + 1), Name: name, Enabled: true})
			}
			return out, nil
		}
	}
	r, e = c.request(ctx, http.MethodGet, "/ISAPI/Streaming/channels", nil, "application/xml")
	if e != nil {
		return nil, e
	}
	defer r.Body.Close()
	var v streamingList
	if e = xml.NewDecoder(r.Body).Decode(&v); e != nil {
		return nil, e
	}
	var out []store.Camera
	for _, x := range v.Items {
		if strings.HasSuffix(x.ID, "02") {
			continue
		}
		name := x.Name
		if name == "" {
			name = "Channel " + x.ID
		}
		out = append(out, store.Camera{ChannelID: x.ID, Name: name, Enabled: x.Enabled})
	}
	return out, nil
}

func (c *Client) Probe(ctx context.Context) (DeviceInfo, []store.Camera, Capabilities, error) {
	cap := Capabilities{HTTPS: c.base.Scheme == "https", DigestAuth: true, DownloadModes: []string{"post_xml", "query_parameter", "http_playback_uri"}}
	d, e := c.Device(ctx)
	if e != nil {
		return d, nil, cap, e
	}
	cap.ISAPI = true
	cap.DeviceInfo = true
	cams, e := c.Channels(ctx)
	if e == nil && len(cams) > 0 {
		cap.ChannelDiscovery = true
	} else {
		cap.Notes = append(cap.Notes, "Channel discovery failed: "+errorText(e))
	}
	r, e := c.request(ctx, http.MethodGet, "/ISAPI/ContentMgmt/capabilities", nil, "application/xml")
	if e == nil {
		b, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		r.Body.Close()
		text := strings.ToLower(string(b))
		cap.RecordingSearch = strings.Contains(text, "search") || strings.Contains(text, "contentmgmt")
		cap.DirectDownload = strings.Contains(text, "download")
	}
	if !cap.RecordingSearch {
		cap.RecordingSearch = true
		cap.Notes = append(cap.Notes, "Recording search assumed from ContentMgmt availability; verified on first search")
	}
	if !cap.DirectDownload {
		cap.DirectDownload = true
		cap.Notes = append(cap.Notes, "Direct download uses firmware fallbacks and is verified on first export")
	}
	return d, cams, cap, nil
}
func errorText(e error) string {
	if e == nil {
		return "unknown response"
	}
	return e.Error()
}

type searchResponse struct {
	Num    int          `xml:"numOfMatches"`
	Status string       `xml:"responseStatusStrg"`
	Items  []searchItem `xml:"matchList>searchMatchItem"`
}
type searchItem struct {
	TrackID     string `xml:"trackID"`
	Start       string `xml:"timeSpan>startTime"`
	End         string `xml:"timeSpan>endTime"`
	PlaybackURI string `xml:"mediaSegmentDescriptor>playbackURI"`
	ContentType string `xml:"mediaSegmentDescriptor>contentType"`
	Size        int64  `xml:"mediaSegmentDescriptor>fileSize"`
}

func (c *Client) Search(ctx context.Context, channelIDs []string, start, end time.Time) ([]store.Recording, error) {
	const pageSize = 40

	type timeSpan struct {
		Start string `xml:"startTime"`
		End   string `xml:"endTime"`
	}

	type searchDescription struct {
		XMLName              xml.Name   `xml:"CMSearchDescription"`
		SearchID             string     `xml:"searchID"`
		TrackIDs             []string   `xml:"trackIDList>trackID"`
		TimeSpans            []timeSpan `xml:"timeSpanList>timeSpan"`
		ContentTypes         []string   `xml:"contentTypeList>contentType"`
		MaxResults           int        `xml:"maxResults"`
		SearchResultPosition int        `xml:"searchResultPostion"`
	}

	var out []store.Recording

	for _, track := range channelIDs {
		position := 0

		for {
			reqBody := searchDescription{
				SearchID: randomGUID(),
				TrackIDs: []string{track},
				TimeSpans: []timeSpan{
					{
						Start: start.UTC().Format(time.RFC3339),
						End:   end.UTC().Format(time.RFC3339),
					},
				},
				ContentTypes:         []string{"video"},
				MaxResults:           pageSize,
				SearchResultPosition: position,
			}

			xmlBody, err := xml.Marshal(reqBody)
			if err != nil {
				return nil, fmt.Errorf("marshal search track %s: %w", track, err)
			}

			body := append([]byte(xml.Header), xmlBody...)

			r, err := c.request(
				ctx,
				http.MethodPost,
				"/ISAPI/ContentMgmt/search",
				body,
				"application/xml",
			)
			if err != nil {
				return nil, fmt.Errorf("search track %s position %d: %w", track, position, err)
			}

			var v searchResponse
			err = xml.NewDecoder(r.Body).Decode(&v)
			r.Body.Close()
			if err != nil {
				return nil, fmt.Errorf(
					"decode search track %s position %d: %w",
					track,
					position,
					err,
				)
			}

			for _, x := range v.Items {
				out = append(out, store.Recording{
					ChannelID:   x.TrackID,
					PlaybackURI: x.PlaybackURI,
					Start:       x.Start,
					End:         x.End,
					ContentType: x.ContentType,
					Size:        x.Size,
				})
			}

			if len(v.Items) < pageSize {
				break
			}

			position += pageSize
		}
	}

	return out, nil
}

// Snapshot fetches a single live JPEG preview image for a channel via
// GET /ISAPI/Streaming/channels/{channelID}/picture. channelID is the same
// track ID used for search/download (e.g. "101", "301") — Hikvision serves
// the main-stream still image at that same channel identifier.
func (c *Client) Snapshot(ctx context.Context, channelID string) ([]byte, string, error) {
	r, e := c.request(ctx, http.MethodGet, "/ISAPI/Streaming/channels/"+url.PathEscape(channelID)+"/picture", nil, "")
	if e != nil {
		return nil, "", e
	}
	defer r.Body.Close()
	contentType := r.Header.Get("Content-Type")
	if contentType == "" || !strings.HasPrefix(contentType, "image/") {
		contentType = "image/jpeg"
	}
	b, e := io.ReadAll(io.LimitReader(r.Body, 8<<20))
	if e != nil {
		return nil, "", e
	}
	if len(b) == 0 {
		return nil, "", fmt.Errorf("recorder returned an empty snapshot")
	}
	return b, contentType, nil
}

func (c *Client) OpenDownload(ctx context.Context, playbackURI string) (*http.Response, error) {
	body := []byte(`<?xml version="1.0" encoding="UTF-8"?><downloadRequest><playbackURI>` + xmlEscape(playbackURI) + `</playbackURI></downloadRequest>`)
	if r, e := c.request(ctx, http.MethodPost, "/ISAPI/ContentMgmt/download", body, "application/xml"); e == nil {
		return r, nil
	}
	path := "/ISAPI/ContentMgmt/download?playbackURI=" + url.QueryEscape(playbackURI)
	if r, e := c.request(ctx, http.MethodGet, path, nil, "application/octet-stream"); e == nil {
		return r, nil
	}
	u, e := url.Parse(playbackURI)
	if e == nil && (u.Scheme == "http" || u.Scheme == "https") {
		return c.requestAbsolute(ctx, http.MethodGet, u, nil, "application/octet-stream")
	}
	return nil, fmt.Errorf("direct download rejected by all ISAPI HTTP fallbacks; firmware may require SDK/HCNetSDK port 8000 or RTSP real-time capture")
}

func (c *Client) request(ctx context.Context, method, path string, body []byte, contentType string) (*http.Response, error) {
	u := c.base.ResolveReference(&url.URL{Path: path})
	if strings.Contains(path, "?") {
		u, _ = url.Parse(c.base.String() + path)
	}
	return c.requestAbsolute(ctx, method, u, body, contentType)
}
func (c *Client) requestAbsolute(ctx context.Context, method string, u *url.URL, body []byte, contentType string) (*http.Response, error) {
	makeReq := func() (*http.Request, error) {
		r, e := http.NewRequestWithContext(ctx, method, u.String(), bytes.NewReader(body))
		if e == nil && len(body) > 0 {
			r.Header.Set("Content-Type", contentType)
		}
		r.Header.Set("Accept", "application/xml, application/octet-stream")
		return r, e
	}
	req, e := makeReq()
	if e != nil {
		return nil, e
	}
	resp, e := c.http.Do(req)
	if e != nil {
		return nil, e
	}
	if resp.StatusCode != http.StatusUnauthorized {
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			defer resp.Body.Close()
			b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
			return nil, fmt.Errorf("ISAPI %s %s: HTTP %d: %s", method, u.Path, resp.StatusCode, strings.TrimSpace(string(b)))
		}
		return resp, nil
	}
	challenge := resp.Header.Get("WWW-Authenticate")
	resp.Body.Close()
	auth, e := digestHeader(challenge, c.username, c.password, method, u.RequestURI())
	if e != nil {
		return nil, e
	}
	req, e = makeReq()
	if e != nil {
		return nil, e
	}
	req.Header.Set("Authorization", auth)
	resp, e = c.http.Do(req)
	if e != nil {
		return nil, e
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		defer resp.Body.Close()
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("ISAPI %s %s: HTTP %d: %s", method, u.Path, resp.StatusCode, strings.TrimSpace(string(b)))
	}
	return resp, nil
}

var digestKV = regexp.MustCompile(`([a-zA-Z]+)=(?:"([^"]*)"|([^, ]+))`)

func digestHeader(challenge, user, password, method, uri string) (string, error) {
	if !strings.HasPrefix(strings.ToLower(challenge), "digest ") {
		return "", fmt.Errorf("device did not offer Digest authentication")
	}
	m := map[string]string{}
	for _, p := range digestKV.FindAllStringSubmatch(challenge, -1) {
		v := p[2]
		if v == "" {
			v = p[3]
		}
		m[strings.ToLower(p[1])] = v
	}
	realm, nonce := m["realm"], m["nonce"]
	if realm == "" || nonce == "" {
		return "", fmt.Errorf("invalid Digest challenge")
	}
	qop := ""
	for _, v := range strings.Split(m["qop"], ",") {
		if strings.TrimSpace(v) == "auth" {
			qop = "auth"
			break
		}
	}
	cnonce := randomHex(8)
	nc := "00000001"
	ha1 := md5hex(user + ":" + realm + ":" + password)
	ha2 := md5hex(method + ":" + uri)
	response := md5hex(ha1 + ":" + nonce + ":" + ha2)
	extra := ""
	if qop != "" {
		response = md5hex(ha1 + ":" + nonce + ":" + nc + ":" + cnonce + ":" + qop + ":" + ha2)
		extra = fmt.Sprintf(", qop=%s, nc=%s, cnonce=\"%s\"", qop, nc, cnonce)
	}
	return fmt.Sprintf(`Digest username="%s", realm="%s", nonce="%s", uri="%s", response="%s", algorithm=MD5%s`, user, realm, nonce, uri, response, extra), nil
}
func md5hex(v string) string { s := md5.Sum([]byte(v)); return hex.EncodeToString(s[:]) }
func randomGUID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)

	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80

	return fmt.Sprintf(
		"%08x-%04x-%04x-%04x-%012x",
		b[0:4],
		b[4:6],
		b[6:8],
		b[8:10],
		b[10:16],
	)
}

func randomHex(n int) string { b := make([]byte, n); _, _ = rand.Read(b); return hex.EncodeToString(b) }
func xmlEscape(v string) string {
	var b strings.Builder
	_ = xml.EscapeText(&b, []byte(v))
	return b.String()
}
