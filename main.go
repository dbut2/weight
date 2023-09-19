package main

import (
	"fmt"
	"net/http"
	"os"

	"github.com/gin-gonic/gin"
)

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	e := gin.Default()

	e.POST("/receive", func(c *gin.Context) {
		data := map[string]any{}
		err := c.BindJSON(&data)
		if err != nil {
			_ = c.Error(err)
			c.Status(http.StatusInternalServerError)
			return
		}
		fmt.Println(data)
	})

	e.GET("/")

	err := http.ListenAndServe(":"+port, e)
	if err != nil {
		panic(err.Error())
	}
}
