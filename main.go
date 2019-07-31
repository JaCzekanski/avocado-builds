package main

import (
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/gorilla/handlers"
	"github.com/gorilla/mux"
)

const (
	baseURL       = "/d"
	dataDir       = "data"
	metadataFile  = ".metadata.json"
	maxUploadSize = 100 * 1024 * 1024
	badgeURL      = "https://img.shields.io/badge"
)

var badgeCache = make(map[string][]byte)
var apiToken string

type errorResponse struct {
	Error []string `json:"error"`
}

type healthResponse struct {
	Status string `json:"status"`
}

type Metadata struct {
	Revision  string            `json:"revision"`
	Branch    string            `json:"branch"`
	Author    string            `json:"author"`
	Message   string            `json:"message"`
	Date      time.Time         `json:"date"`
	Artifacts map[string]string `json:"-"`
}

type BuildsData struct {
	BaseURL        string
	Commits        []Metadata
	UpdatedAt      string
	GenerationTime time.Duration
}

func writeJson(w http.ResponseWriter, v interface{}) {
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Fatal(err)
	}
}

func httpErrors(w http.ResponseWriter, msgs []string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusBadRequest)
	writeJson(w, errorResponse{
		Error: msgs,
	})
	log.Print(msgs)
}

func httpError(w http.ResponseWriter, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusBadRequest)
	writeJson(w, errorResponse{
		Error: []string{
			msg,
		},
	})
	log.Print(msg)
}

func checkAuthorization(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		authorization := r.Header.Get("Authorization")
		token := strings.TrimLeft(authorization, "Bearer ")
		if token != apiToken {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			writeJson(w, errorResponse{
				Error: []string{"Invalid authentication token"},
			})
			return
		}

		h.ServeHTTP(w, r)
	}
}

func authorized(h http.HandlerFunc) http.HandlerFunc {
	return checkAuthorization(h)
}

func healthCheck(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	writeJson(w, healthResponse{
		Status: "ok",
	})
}

func readMetadata(rev string) (Metadata, error) {
	f, err := os.Open(filepath.Join(dataDir, rev, metadataFile))
	if err != nil {
		return Metadata{}, err
	}
	defer f.Close()

	bytes, _ := ioutil.ReadAll(f)

	var metadata Metadata
	json.Unmarshal(bytes, &metadata)

	metadata.Artifacts = make(map[string]string)

	return metadata, nil
}

func getPlatform(file string) string {
	platformRegex := regexp.MustCompile("-(.*?)-")
	matches := platformRegex.FindStringSubmatch(file)

	if len(matches) != 2 {
		return ""
	}
	return matches[1]
}

func generateListing() []Metadata {
	revs, err := ioutil.ReadDir(dataDir)
	if err != nil {
		log.Fatal(err)
	}

	listing := []Metadata{}

	for _, rev := range revs {
		if !rev.IsDir() {
			continue
		}

		metadata, err := readMetadata(rev.Name())
		if err != nil {
			continue
		}

		artifacts, err := ioutil.ReadDir(filepath.Join(dataDir, rev.Name()))
		if err != nil {
			log.Fatal(err)
		}
		for _, artifact := range artifacts {
			name := artifact.Name()
			if artifact.IsDir() || name == metadataFile {
				continue
			}

			platform := getPlatform(artifact.Name())
			if len(platform) == 0 {
				continue
			}

			metadata.Artifacts[platform] = artifact.Name()
		}

		listing = append(listing, metadata)
	}

	sort.Slice(listing, func(i, j int) bool {
		return listing[i].Date.After(listing[j].Date)
	})

	return listing
}

func getIndex(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	listing := generateListing()
	generationTime := time.Since(start)

	data := BuildsData{
		BaseURL:        baseURL,
		Commits:        listing,
		UpdatedAt:      time.Now().Format("2006-01-02 15:04:05 MST"),
		GenerationTime: generationTime,
	}
	tmpl := template.Must(template.ParseFiles("template/builds.html"))
	tmpl.Execute(w, data)
}

func artifactUpload(w http.ResponseWriter, r *http.Request) {
	msgs := []string{}

	date, err := time.Parse("2006-01-02T15:04:05-07:00", r.Header.Get("X-Commit-Date"))
	if err != nil {
		msgs = append(msgs, "Missing X-Commit-Date header or invalid date format")
	}
	metadata := Metadata{
		Revision: r.Header.Get("X-Commit-Revision"),
		Branch:   r.Header.Get("X-Commit-Branch"),
		Author:   r.Header.Get("X-Commit-Author"),
		Message:  r.Header.Get("X-Commit-Message"),
		Date:     date,
	}

	// TODO: More validation, strip invalid ?
	if len(metadata.Revision) == 0 {
		msgs = append(msgs, "Missing X-Commit-Revision header")
	}
	if len(metadata.Author) == 0 {
		msgs = append(msgs, "Missing X-Commit-Author header")
	}
	if len(metadata.Message) == 0 {
		msgs = append(msgs, "Missing X-Commit-Message header")
	}
	if len(metadata.Branch) == 0 {
		msgs = append(msgs, "Missing X-Commit-Branch header")
	}
	if len(msgs) != 0 {
		httpErrors(w, msgs)
		return
	}

	err = r.ParseMultipartForm(maxUploadSize)
	if err != nil {
		httpError(w, err.Error())
		return
	}

	path := filepath.Join(dataDir, metadata.Revision)
	if _, err := os.Stat(path); os.IsNotExist(err) {
		err = os.MkdirAll(path, 0755)
		if err != nil {
			httpError(w, "Cannot save file")
			log.Fatal(err)
			return
		}
	}

	fMeta, err := os.Create(filepath.Join(path, metadataFile))
	if err != nil {
		httpError(w, "Cannot save metadata")
		log.Fatal(err)
		return
	}
	defer fMeta.Close()
	raw, err := json.MarshalIndent(metadata, "", "  ")
	if _, err = fMeta.Write(raw); err != nil {
		log.Fatal(err)
	}

	// TODO: Handle file array
	src, header, err := r.FormFile("file")
	if err != nil {
		httpError(w, err.Error())
		return
	}
	defer src.Close()

	dst, err := os.Create(filepath.Join(path, header.Filename))
	if err != nil {
		httpError(w, "Cannot save artifact")
		log.Fatal(err)
		return
	}
	defer dst.Close()

	if _, err = io.Copy(dst, src); err != nil {
		log.Fatal(err)
	}

	log.Printf("Uploading %s (rev: %s)", header.Filename, metadata.Revision)

	w.WriteHeader(http.StatusCreated)
}

func getLatestArtifactForPlatform(w http.ResponseWriter, r *http.Request) {
	// TODO: Cache
	// TODO: Filter by branch
	listing := generateListing()

	params := mux.Vars(r)
	platform := params["platform"]

	for _, commit := range listing {
		if artifact, ok := commit.Artifacts[platform]; ok {
			// TODO: ugly way
			http.Redirect(w, r, fmt.Sprintf("https://%s%s/%s/%s", r.Host, baseURL, commit.Revision, artifact), http.StatusTemporaryRedirect)
		}
	}
	http.NotFound(w, r)
}

func getBadge(status bool) ([]byte, error) {
	title := "build"
	var msg string
	var color string
	if status {
		msg = "passing"
		color = "brightgreen"
	} else {
		msg = "failed"
		color = "red"
	}
	url := fmt.Sprintf("%s/%s-%s-%s", badgeURL, title, msg, color)

	if image, ok := badgeCache[url]; ok {
		return image, nil
	}

	r, err := http.Get(url)
	if err != nil {
		return nil, err
	}

	image, err := ioutil.ReadAll(r.Body)
	if err != nil {
		return nil, err
	}

	badgeCache[url] = image

	return image, nil
}
func getBuildStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Add("Cache-Control", "no-cache")
	// TODO: Cache
	listing := generateListing()

	params := mux.Vars(r)
	platform := params["platform"]

	if len(listing) == 0 {
		http.NotFound(w, r)
		return
	}

	latestCommit := listing[0]
	// TODO: Status - pending
	_, ok := latestCommit.Artifacts[platform]

	badge, err := getBadge(ok)
	if err != nil {
		log.Print(err)
		http.Error(w, "Cannot download badge", http.StatusBadRequest)
		return
	}

	w.Header().Set("Content-Type", "image/svg+xml")
	w.Write(badge)
}

func main() {
	apiToken = os.Getenv("API_TOKEN")
	if len(apiToken) == 0 {
		log.Fatalln("API_TOKEN env not specified")
	}
	port := os.Getenv("PORT")
	if len(port) == 0 {
		log.Fatalln("PORT env not specified")
	}

	if _, err := os.Stat(dataDir); os.IsNotExist(err) {
		err = os.MkdirAll(dataDir, 0755)
		if err != nil {
			log.Fatal(err)
			return
		}
	}

	r := mux.NewRouter()
	r.HandleFunc("/", getIndex).Methods("GET")
	r.HandleFunc("/index.htm", getIndex).Methods("GET")
	r.HandleFunc("/index.html", getIndex).Methods("GET")
	r.HandleFunc("/api/health-check", healthCheck).Methods("GET")
	r.HandleFunc("/api/upload", authorized(artifactUpload)).Methods("POST")
	r.HandleFunc("/latest/{platform}", getLatestArtifactForPlatform).Methods("GET")
	r.HandleFunc("/status/{platform}", getBuildStatus).Methods("GET")

	log.Printf("Running server at port %s", port)
	log.Fatal(http.ListenAndServe(fmt.Sprintf(":%s", port), handlers.CombinedLoggingHandler(os.Stdout, handlers.ProxyHeaders(r))))
}
