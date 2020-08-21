package main

import (
	"encoding/json"
	"flag"
	"io/ioutil"
	"log"
	"net/http"
	"net/url"
	"os"
	"runtime/pprof"
	"strings"
	"time"

	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
	_ "github.com/heroku/x/hmetrics/onload"
	"gopkg.in/vmarkovtsev/go-lcss.v1"
	"mvdan.cc/xurls/v2"
)

const DEBUG = false

const cofactsGqlQuery = `
query($text: String) {
  ListArticles(
	filter: { moreLikeThis: { like: $text } }
	orderBy: [{ _score: DESC }]
	first: 4
  ) {
	edges {
	  node {
		id
		text
		hyperlinks {
		  url
		}
		articleReplies {
		  reply {
			id
			text
			type
			reference
		  }
		}
	  }
	}
  }
}`

// TODO: get the whole Schema from https://cofacts-api.g0v.tw/graphql
type Hyperlink struct {
	Url string `json:"url"`
}

type ArticleReply struct {
	Id        string `json:"id"`
	Text      string `json:"text"`
	Type      string `json:"type"`
	Reference string `json:"reference"`
}

type ArticleReplies struct {
	Reply ArticleReply `json:"reply"`
}

type Node struct {
	Id             string           `json:"id"`
	Text           string           `json:"text"`
	Hyperlinks     []Hyperlink      `json:"hyperlinks"`
	ArticleReplies []ArticleReplies `json:"articleReplies"`

	// Added by this server to indicate whether the article matches the search query.
	// We should just filter in the final version, but for development it will be
	// useful to see what results we get from Cofacts and whether the server accepts
	// or rejects them.
	IsMatch bool `json:"ismatch"`
}

type Edge struct {
	Node Node `json:"node"`
}

type ArticleList struct {
	Edges []Edge `json:"edges"`
}

type Data struct {
	ListArticles ArticleList `json:"ListArticles"`
}

type CofactResponse struct {
	Data Data `json:"data"`
}

var cpuprofile = flag.String("cpuprofile", "", "write cpu profile to file")

func main() {
	if DEBUG {
		flag.Parse()
		if *cpuprofile != "" {
			f, err := os.Create(*cpuprofile)
			if err != nil {
				log.Fatal(err)
			}
			pprof.StartCPUProfile(f)
			defer pprof.StopCPUProfile()
		}
	}

	port := os.Getenv("PORT")

	if port == "" {
		log.Fatal("$PORT must be set")
	}

	router := gin.Default()
	router.Use(gin.Logger())
	router.LoadHTMLGlob("templates/*.tmpl.html")
	router.Static("/static", "static")

	router.Use(cors.New(cors.Config{
		AllowMethods:    []string{"GET"},
		AllowHeaders:    []string{"Origin", "text"},
		ExposeHeaders:   []string{"Content-Length"},
		AllowAllOrigins: true,
		MaxAge:          48 * time.Hour,
	}))

	router.GET("/cofacts", handleCofactsRequestWithContentInHeader)
	router.POST("/cofacts", handleCofactsRequestWithContentInBody)

	if DEBUG {
		srv := &http.Server{
			Addr:    ":" + port,
			Handler: router,
		}
		router.POST("/quit", func(c *gin.Context) {
			srv.Shutdown(nil)
		})
		router.GET("/quit", func(c *gin.Context) {
			srv.Shutdown(nil)
		})
		srv.ListenAndServe()
	} else {
		router.Run(":" + port)
	}
}

func isEquivalent(url1 string, url2 string) bool {
	u1, err := url.Parse(url1)
	if err != nil {
		panic(err)
	}
	u2, err := url.Parse(url2)
	if err != nil {
		panic(err)
	}
	if u1.Host != u2.Host {
		return false
	}
	if strings.TrimRight(u1.Path, "/") != strings.TrimRight(u2.Path, "/") {
		return false
	}
	q1 := u1.Query()
	q2 := u2.Query()
	for k, vs := range q1 {
		for _, v1 := range vs {
			var found = false
			for _, v2 := range q2[k] {
				if v1 == v2 {
					found = true
					break
				}
			}
			if !found {
				return false
			}
		}
	}
	return true
}

func exist_same_url(node *Node, request_urls []string) bool {
	for _, hyperlink := range node.Hyperlinks {
		node_url := hyperlink.Url
		for _, request_url := range request_urls {
			if isEquivalent(node_url, request_url) {
				return true
			}
		}
	}
	return false
}

func removeWhitespace(s string) string {
	s = strings.ReplaceAll(s, "\n", "")
	s = strings.ReplaceAll(s, "\r", "")
	s = strings.ReplaceAll(s, "\t", "")
	s = strings.ReplaceAll(s, " ", "")
	return s
}

func chunk(s []byte, chunkSize int) [][]byte {
	var chunks [][]byte

	if len(s) == 0 {
		return make([][]byte, 0)
	}

	for i := 0; i < len(s); i += chunkSize {
		nn := i + chunkSize
		if nn > len(s) {
			nn = len(s)
		}
		chunks = append(chunks, s[i:nn])
	}
	return chunks
}

func lcss_chunked(a []byte, b []byte) []byte {
	if len(a) > len(b) {
		return lcss_chunked(b, a)
	}

	if len(a)*6 > len(b) {
		// chunking is only faster if there is a large size difference.
		// (didn't bother to figure out the exact threshold)
		return lcss.LongestCommonSubstring(a, b)
	}

	// The performance of lcss.LongestCommonSubstring seems to be quadratic,
	// despite what the Github page says. If one string is significantly shorter
	// than the other, then it's faster to chunk the larger string and do
	// several calls to lcss.LongestCommonSubstring.
	// We split the largest string in chunks twice the size of the smaller,
	// and do this twice with the second batch offset by the length of the smaller
	// string to account for cases where the LCSS spills over into the next chunk.
	// So if the smaller string is 10 bytes, we chunk the larger into the following
	// blocks: [ 0:20], [20:40], [40:60] etc,
	//     and [10:30], [30:50], [50:70] etc.
	var best []byte = make([]byte, 0)
	var best_len int = 0

	chunks := chunk(b, 2*len(a))
	for _, chunk := range chunks {
		current := lcss.LongestCommonSubstring(a, chunk)
		if len(current) > best_len {
			best = current
			best_len = len(current)
		}
	}

	chunks = chunk(b[len(a):], 2*len(a))
	for _, chunk := range chunks {
		current := lcss.LongestCommonSubstring(a, chunk)
		if len(current) > best_len {
			best = current
			best_len = len(current)
		}
	}

	return best
}

func handleCofactsGet(c *gin.Context) {
	text := c.DefaultQuery("text", "")

	handleCofacts(c, text)
}

func handleCofactsRequestWithContentInHeader(c *gin.Context) {
	body, err := url.QueryUnescape(c.Request.Header.Get("text"))
	if err != nil {
		c.String(http.StatusInternalServerError, "error:", err)
		return
	}

	handleCofacts(c, body)
}

func handleCofactsRequestWithContentInBody(c *gin.Context) {
	body, err := c.GetRawData()
	if err != nil {
		c.String(http.StatusInternalServerError, "error:", err)
		return
	}

	handleCofacts(c, string(body))
}

func handleCofacts(c *gin.Context, text string) {
	// Call the Cofacts api
	respText, err := callCofactsApi(text)
	if err != nil {
		c.String(http.StatusInternalServerError, "error:", err)
		return
	}

	// Convert to CofactResponse struct
	var respData CofactResponse
	err = json.Unmarshal([]byte(respText), &respData)
	if err != nil {
		c.String(http.StatusInternalServerError, "error:", err)
		return
	}

	// Follow roughly the same filter approach as Aunt Meiyu
	rxStrict := xurls.Strict()
	request_urls := rxStrict.FindAllString(text, -1)
	if len(request_urls) > 0 {
		// If there's a url in the text, it must be in the article
		for i := range respData.Data.ListArticles.Edges {
			node := &respData.Data.ListArticles.Edges[i].Node
			node.IsMatch = exist_same_url(node, request_urls)
		}
	} else {
		// Todo: should use tf-idf, but for an early demo this is good enough
		for i := range respData.Data.ListArticles.Edges {
			node := &respData.Data.ListArticles.Edges[i].Node

			// strip any whitespace for comparison
			a := removeWhitespace(text)
			b := removeWhitespace(node.Text)
			common := lcss_chunked([]byte(a), []byte(b))
			// Match if least 25 characters, or 80% of the query text in common
			node.IsMatch = (len(common) > 25) || (len(common)*100/len(text) >= 80)
		}
	}

	c.Header("Cache-Control", "public,max-age=86400")
	c.JSON(http.StatusOK, respData)
}

func callCofactsApi(text string) (string, error) {
	type CofactsRequestVariables struct {
		Text string `json:"text"`
	}

	type CofactsRequest struct {
		Query     string                  `json:"query"`
		Variables CofactsRequestVariables `json:"variables"`
	}

	cofactsQuery := CofactsRequest{
		Query:     cofactsGqlQuery,
		Variables: CofactsRequestVariables{Text: text},
	}

	body, err := json.Marshal(&cofactsQuery)
	if err != nil {
		return "", err
	}

	resp, err := http.Post(
		"https://cofacts-api.g0v.tw/graphql",
		"application/json",
		strings.NewReader(string(body)))
	if err != nil {
		return "", err
	}

	defer resp.Body.Close()
	respText, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	return string(respText), nil
}
