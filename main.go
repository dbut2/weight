package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"os"
	"sync"
	"time"

	"cloud.google.com/go/datastore"
	secretmanager "cloud.google.com/go/secretmanager/apiv1"
	"cloud.google.com/go/secretmanager/apiv1/secretmanagerpb"
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

var dsc func() *datastore.Client
var smc func() *secretmanager.Client
var fbc func() *fitbit.Session

func main() {
	setupConfiguration()
	e := setupRoutes()
	startServer(e)
}

func setupConfiguration() {
	dsc = sync.OnceValue(func() *datastore.Client {
		client, err := datastore.NewClient(context.Background(), os.Getenv("PROJECT_ID"))
		if err != nil {
			panic(err.Error())
		}
		return client
	})

	smc = sync.OnceValue(func() *secretmanager.Client {
		client, err := secretmanager.NewClient(context.Background())
		if err != nil {
			panic(err.Error())
		}
		return client
	})

	fbc = sync.OnceValue(func() *fitbit.Session {
		client := fitbit.New(fitbit.Config{
			ClientID:     os.Getenv("FITBIT_CLIENT"),
			ClientSecret: os.Getenv("FITBIT_SECRET"),
			RedirectURL:  os.Getenv("FITBIT_REDIRECT"),
			Scopes:       []fitbit.Scope{fitbit.ScopeWeight},
			Locale:       "en-au",
		})
		client.SetToken(getToken())
		client.TokenChange = setToken

		return client
	})
}

func getToken() *oauth2.Token {
	secret := os.Getenv("FITBIT_TOKEN_SECRET")

	resp, err := smc().AccessSecretVersion(context.Background(), &secretmanagerpb.AccessSecretVersionRequest{
		Name: secret + "/versions/latest",
	})
	if err != nil {
		panic(err.Error())
	}

	bytes := resp.GetPayload().Data
	token := &oauth2.Token{}
	err = json.Unmarshal(bytes, token)
	if err != nil {
		panic(err.Error())
	}

	return token
}

func setToken(token *oauth2.Token) {
	secret := os.Getenv("FITBIT_TOKEN_SECRET")

	bytes, err := json.Marshal(token)
	if err != nil {
		panic(err.Error())
	}

	_, err = smc().AddSecretVersion(context.Background(), &secretmanagerpb.AddSecretVersionRequest{
		Parent: secret,
		Payload: &secretmanagerpb.SecretPayload{
			Data: bytes,
		},
	})
	if err != nil {
		panic(err.Error())
	}
}

//go:embed weight.html
var weightTemplate string

func setupRoutes() *gin.Engine {
	e := gin.Default()

	e.SetHTMLTemplate(template.Must(template.New("weight").Parse(weightTemplate)))

	e.POST("/receive", receivePostHandler)
	e.GET("/receive", receiveGetHandler)
	e.GET("/batch", batchHandler)
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
	bw, err := fbc().BodyWeightLogByDay(sub.Date)
	if err != nil {
		handleError(c, err)
		return
	}
	fmt.Printf("fetched %d weights\n", len(bw.Weight))
	fmt.Printf("%+v\n", bw.Weight)

	var existingWeights []weight
	keys, err := dsc().GetAll(c, datastore.NewQuery("Weight").FilterField("Date", "=", sub.Date), &existingWeights)
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

		err = dsc().Delete(c, keys[i])
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

		_, err = dsc().Put(c, datastore.IDKey("Weight", ww.FitbitLogID, nil), &ww)
		if err != nil {
			handleError(c, err)
			return
		}
	}
}

func batchHandler(c *gin.Context) {
	start, end := c.Query("start"), c.Query("end")

	slog.Info("batch loading", slog.String("startDate", start), slog.String("endDate", end))

	bw, err := fbc().BodyWeightLogByDateRange(start, end)
	if err != nil {
		handleError(c, err)
		return
	}

	slog.Info("fetched weights", slog.Int("count", len(bw.Weight)), slog.Any("blob", bw))

	for _, w := range bw.Weight {
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

		_, err = dsc().Put(c, datastore.IDKey("Weight", ww.FitbitLogID, nil), &ww)
		if err != nil {
			handleError(c, err)
			return
		}
	}

	c.String(http.StatusOK, fmt.Sprintf("%d weights loaded", len(bw.Weight)))
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
	_, err := dsc().GetAll(c, datastore.NewQuery("Weight").Order("-Datetime"), &weights)
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
