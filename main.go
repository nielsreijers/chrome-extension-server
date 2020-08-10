package main

import (
	"encoding/json"
	"io/ioutil"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
	_ "github.com/heroku/x/hmetrics/onload"
	"github.com/russross/blackfriday"
	"gopkg.in/vmarkovtsev/go-lcss.v1"
	"mvdan.cc/xurls/v2"
)

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

func main() {
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
		AllowHeaders:    []string{"Origin"},
		ExposeHeaders:   []string{"Content-Length"},
		AllowAllOrigins: true,
	}))

	router.GET("/", func(c *gin.Context) {
		c.HTML(http.StatusOK, "index.tmpl.html", nil)
	})

	router.GET("/mark", func(c *gin.Context) {
		c.String(http.StatusOK, string(blackfriday.Run([]byte("**hi!**"))))
	})

	router.GET("/cofacts", handleCofactsRequestWithContentInBody)
	router.POST("/cofacts", handleCofactsRequestWithContentInBody)

	router.Run(":" + port)
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

func handleCofactsGet(c *gin.Context) {
	text := c.DefaultQuery("text", "")

	handleCofacts(c, text)
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
			common := lcss.LongestCommonSubstring([]byte(a), []byte(b))
			// Match if least 25 characters, or 80% of the query text in common
			// TODO strip newlines
			node.IsMatch = (len(common) > 25) || (len(common)*100/len(text) >= 80)
		}
	}

	// Convert back to json
	respTextBytes, err := json.Marshal(&respData)
	if err != nil {
		c.String(http.StatusInternalServerError, "error:", err)
		return
	}

	c.Header("Cache-Control", "public,max-age=86400")
	c.String(http.StatusOK, string(respTextBytes))
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
