package main

import (
	"context"
	"embed"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
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
	"golang.org/x/sync/errgroup"
	"google.golang.org/api/iterator"
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

func (w weight) DisplayDate() string {
	return w.Datetime.Format("Jan 2")
}

func (w weight) JSDate() string {
	return w.Datetime.Format("2006-01-02 15:04:05")
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
			Locale:       "en-AU",
		})
		client.SetToken(getToken())
		client.TokenChange = setToken

		return client
	})
}

// getToken fetches the current token from secret manager.
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

// setToken sets the current token in secret manager, destroying all older versions.
func setToken(token *oauth2.Token) {
	secret := os.Getenv("FITBIT_TOKEN_SECRET")

	bytes, err := json.Marshal(token)
	if err != nil {
		panic(err.Error())
	}

	newVersion, err := smc().AddSecretVersion(context.Background(), &secretmanagerpb.AddSecretVersionRequest{
		Parent: secret,
		Payload: &secretmanagerpb.SecretPayload{
			Data: bytes,
		},
	})
	if err != nil {
		panic(err.Error())
	}

	secretVersions := smc().ListSecretVersions(context.Background(), &secretmanagerpb.ListSecretVersionsRequest{
		Parent: secret,
		Filter: "NOT state:DESTROYED",
	})

	for {
		nextVersion, err := secretVersions.Next()
		if errors.Is(err, iterator.Done) {
			break
		}
		if err != nil {
			panic(err.Error())
		}

		if nextVersion.GetName() == newVersion.GetName() {
			continue
		}

		_, err = smc().DestroySecretVersion(context.Background(), &secretmanagerpb.DestroySecretVersionRequest{
			Name: nextVersion.GetName(),
			Etag: nextVersion.GetEtag(),
		})
		if err != nil {
			panic(err.Error())
		}
	}
}

//go:embed pages/*.html
var pages embed.FS

func setupRoutes() *gin.Engine {
	e := gin.Default()

	t := must(template.New("pages").ParseFS(pages, "pages/*.html"))

	e.SetHTMLTemplate(t)

	e.POST("/receive", receivePostHandler)
	e.GET("/receive", receiveGetHandler)
	e.GET("/batch", batchHandler)
	e.POST("/health", healthPostHandler)
	e.GET("/health", healthGetHandler)
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
		err = syncSub(c, sub)
		if err != nil {
			handleError(c, err)
			return
		}
	}
}

func syncSub(ctx context.Context, sub fitbit.Subscription) error {
	bw, err := fbc().BodyWeightLogByDay(sub.Date)
	if err != nil {
		return err
	}
	fmt.Printf("fetched %d weights\n", len(bw.Weight))
	fmt.Printf("%+v\n", bw.Weight)

	var existingWeights []weight
	keys, err := dsc().GetAll(ctx, datastore.NewQuery("Weight").FilterField("Date", "=", sub.Date), &existingWeights)
	if err != nil {
		return err
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

		err = dsc().Delete(ctx, keys[i])
		if err != nil {
			return err
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
			return err
		}

		ww := weight{
			Date:        w.Date,
			FitbitLogID: w.LogID,
			Time:        w.Time,
			Weight:      w.Weight,
			Datetime:    dt,
		}

		_, err = dsc().Put(ctx, datastore.IDKey("Weight", ww.FitbitLogID, nil), &ww)
		if err != nil {
			return err
		}
	}

	return nil
}

func batchHandler(c *gin.Context) {
	start, end := c.Query("start"), c.Query("end")

	total := 0

	eg := errgroup.Group{}
	for _, dates := range batchDates(start, end) {
		s, e := dates[0], dates[1]
		eg.Go(func() error {
			count, err := syncDates(c, s, e)
			total += count
			return err
		})
	}
	err := eg.Wait()
	if err != nil {
		handleError(c, err)
		return
	}

	c.String(http.StatusOK, fmt.Sprintf("%d weights loaded", total))
}

func syncDates(ctx context.Context, start, end string) (int, error) {
	bw, err := fbc().BodyWeightLogByDateRange(start, end)
	if err != nil {
		return 0, err
	}

	for _, w := range bw.Weight {
		dt, err := time.ParseInLocation("2006-01-02 15:04:05", w.Date+" "+w.Time, time.Local)
		if err != nil {
			return 0, err
		}

		ww := weight{
			Date:        w.Date,
			FitbitLogID: w.LogID,
			Time:        w.Time,
			Weight:      w.Weight,
			Datetime:    dt,
		}

		_, err = dsc().Put(ctx, datastore.IDKey("Weight", ww.FitbitLogID, nil), &ww)
		if err != nil {
			return 0, err
		}
	}

	return len(bw.Weight), nil
}

func batchDates(start, end string) [][2]string {
	layout := "2006-01-02"

	s := must(time.Parse(layout, start))
	e := must(time.Parse(layout, end))

	var ranges [][2]string
	for s.Before(e) {
		b := time.Date(s.Year(), s.Month()+1, 1, 0, 0, 0, 0, s.Location()).Add(-1 * time.Nanosecond)
		if b.After(e) {
			b = e
		}
		ranges = append(ranges, [2]string{s.Format(layout), b.Format(layout)})
		s = b.Add(time.Nanosecond)
	}
	return ranges
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
	_, err := dsc().GetAll(c, datastore.NewQuery("Weight").Order("Datetime"), &weights)
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

	c.HTML(http.StatusOK, "weight.html", gin.H{
		"Weight":  weights[len(weights)-1],
		"Weights": weights,
	})
}

func must[T any](v T, err error) T {
	if err != nil {
		panic(err.Error())
	}
	return v
}
