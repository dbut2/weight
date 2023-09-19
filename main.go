package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"os"
	"time"

	"cloud.google.com/go/datastore"
	"github.com/Thomas2500/go-fitbit"
	"github.com/gin-gonic/gin"
	"golang.org/x/oauth2"
)

type weight struct {
	Date        string
	FitbitLogID int64
	Time        string
	Weight      float64
	Datetime    time.Time
}

func (w weight) WeightParsed() string {
	return fmt.Sprintf("%.1f", w.Weight)
}

var dsc *datastore.Client
var fc *fitbit.Session

func main() {
	setupConfiguration()
	e := setupRoutes()
	startServer(e)
}

func setupConfiguration() {
	var err error
	dsc, err = datastore.NewClient(context.Background(), os.Getenv("PROJECT_ID"))
	if err != nil {
		panic(err.Error())
	}

	token := &oauth2.Token{}
	err = json.Unmarshal([]byte(os.Getenv("FITBIT_TOKEN")), token)
	if err != nil {
		panic(err.Error())
	}

	fc = fitbit.New(fitbit.Config{
		ClientID:     os.Getenv("FITBIT_CLIENT"),
		ClientSecret: os.Getenv("FITBIT_SECRET"),
		RedirectURL:  os.Getenv("FITBIT_REDIRECT"),
		Scopes:       []fitbit.Scope{fitbit.ScopeWeight},
		Locale:       "en-au",
	})
	fc.SetToken(token)
}

//go:embed weight.html
var weightTemplate string

func setupRoutes() *gin.Engine {
	e := gin.Default()

	e.SetHTMLTemplate(template.Must(template.New("weight").Parse(weightTemplate)))

	e.POST("/receive", receivePostHandler)
	e.GET("/receive", receiveGetHandler)
	e.GET("/", rootHandler)

	return e
}

func startServer(e *gin.Engine) {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	err := http.ListenAndServe(":"+port, e)
	if err != nil {
		panic(err)
	}
}

func handleError(c *gin.Context, err error) {
	_ = c.Error(err)
	c.Status(http.StatusInternalServerError)
}

func receivePostHandler(c *gin.Context) {
	var subs []fitbit.Subscription
	err := c.BindJSON(&subs)
	if err != nil {
		handleError(c, err)
		return
	}

	for _, sub := range subs {
		fmt.Printf("syncing sub: %s\n", sub.SubscriptionID)
		fmt.Printf("%+v\n", sub)
		syncSub(c, sub)
	}
}

func syncSub(c *gin.Context, sub fitbit.Subscription) {
	bw, err := fc.BodyWeightLogByDay(sub.Date)
	if err != nil {
		handleError(c, err)
		return
	}
	fmt.Printf("fetched %d weights\n", len(bw.Weight))
	fmt.Printf("%+v\n", bw.Weight)

	var existingWeights []weight
	keys, err := dsc.GetAll(c, datastore.NewQuery("Weight").FilterField("Date", "=", sub.Date), &existingWeights)
	if err != nil {
		handleError(c, err)
		return
	}

	for i, existingWeight := range existingWeights {
		found := false
		for _, w := range bw.Weight {
			if existingWeight.FitbitLogID == w.LogID {
				found = true
				break
			}
		}
		if found {
			continue
		}

		err = dsc.Delete(c, keys[i])
		if err != nil {
			handleError(c, err)
			return
		}
	}

	for _, w := range bw.Weight {
		found := false
		for _, existingWeight := range existingWeights {
			if w.LogID == existingWeight.FitbitLogID {
				found = true
				break
			}
		}
		if found {
			continue
		}

		dt, err := time.Parse("2006-01-02 15:04:05", w.Date+" "+w.Time)
		if err != nil {
			handleError(c, err)
			return
		}

		ww := weight{
			Date:        w.Date,
			FitbitLogID: w.LogID,
			Time:        w.Time,
			Weight:      w.Weight,
			Datetime:    dt,
		}

		_, err = dsc.Put(c, datastore.IDKey("Weight", ww.FitbitLogID, nil), &ww)
		if err != nil {
			handleError(c, err)
			return
		}
	}
}

func receiveGetHandler(c *gin.Context) {
	verification := os.Getenv("FITBIT_VERIFICATION")
	verify := c.Query("verify")

	if verify != verification {
		c.Status(http.StatusNotFound)
		return
	}

	c.Status(http.StatusNoContent)
}

func rootHandler(c *gin.Context) {
	var weights []weight
	_, err := dsc.GetAll(c, datastore.NewQuery("Weight").Order("-Datetime"), &weights)
	if err != nil {
		_ = c.Error(err)
		c.Status(http.StatusInternalServerError)
		return
	}

	if len(weights) == 0 {
		_ = c.Error(errors.New("no weights"))
		c.Status(http.StatusInternalServerError)
		return
	}

	c.HTML(http.StatusOK, "weight", gin.H{
		"Weight": weights[0],
	})
}
