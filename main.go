package main

import (
	"encoding/json"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"strings"

	"github.com/gin-gonic/gin"
	_ "github.com/heroku/x/hmetrics/onload"
	"github.com/russross/blackfriday"
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

func main() {
	port := os.Getenv("PORT")

	if port == "" {
		log.Fatal("$PORT must be set")
	}

	router := gin.New()
	router.Use(gin.Logger())
	router.LoadHTMLGlob("templates/*.tmpl.html")
	router.Static("/static", "static")

	router.GET("/", func(c *gin.Context) {
		c.HTML(http.StatusOK, "index.tmpl.html", nil)
	})

	router.GET("/mark", func(c *gin.Context) {
		c.String(http.StatusOK, string(blackfriday.Run([]byte("**hi!**"))))
	})

	router.GET("/cofacts", func(c *gin.Context) {
		type CofactsRequestVariables struct {
			Text string `json:"text"`
		}

		type CofactsRequest struct {
			Query     string                  `json:"query"`
			Variables CofactsRequestVariables `json:"variables"`
		}

		text := c.DefaultQuery("text", "")

		cofactsQuery := CofactsRequest{
			Query:     cofactsGqlQuery,
			Variables: CofactsRequestVariables{Text: text},
		}
		body, _ := json.Marshal(&cofactsQuery)

		resp, _ := http.Post("https://cofacts-api.g0v.tw/graphql", "application/json", strings.NewReader(string(body)))

		defer resp.Body.Close()
		respText, _ := ioutil.ReadAll(resp.Body)

		c.String(http.StatusOK, string(respText))
	})

	router.Run(":" + port)
}
