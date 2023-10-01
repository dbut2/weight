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
	"sync"
	"time"

	"cloud.google.com/go/datastore"
	secretmanager "cloud.google.com/go/secretmanager/apiv1"
	"cloud.google.com/go/secretmanager/apiv1/secretmanagerpb"
	texporter "github.com/GoogleCloudPlatform/opentelemetry-operations-go/exporter/trace"
	"github.com/Thomas2500/go-fitbit"
	"github.com/gin-gonic/gin"
	"go.opentelemetry.io/contrib/detectors/gcp"
	"go.opentelemetry.io/contrib/instrumentation/github.com/gin-gonic/gin/otelgin"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.21.0"
	"go.opentelemetry.io/otel/trace"
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

var tracer trace.Tracer

func main() {
	exporter, err := texporter.New(texporter.WithProjectID(os.Getenv("PROJECT_ID")))
	if err != nil {
		panic(err.Error())
	}

	res, err := resource.New(context.Background(), resource.WithDetectors(gcp.NewDetector()), resource.WithTelemetrySDK(), resource.WithAttributes(semconv.ServiceNameKey.String("weight")))
	if err != nil {
		panic(err.Error())
	}

	tp := sdktrace.NewTracerProvider(sdktrace.WithBatcher(exporter), sdktrace.WithResource(res))
	defer tp.Shutdown(context.Background())
	otel.SetTracerProvider(tp)

	tracer = tp.Tracer("weight")

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

//go:embed weight.html
var weightTemplate string

func setupRoutes() *gin.Engine {
	e := gin.Default()

	e.Use(otelgin.Middleware("weight"))

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
	ctx, span := tracer.Start(c, "receivePost")
	defer span.End()
	c.Request.WithContext(ctx)

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
	ctx, span := tracer.Start(c, "syncSub")
	defer span.End()
	c.Request.WithContext(ctx)
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
	ctx, span := tracer.Start(c, "batch")
	defer span.End()
	c.Request.WithContext(ctx)

	start, end := c.Query("start"), c.Query("end")

	span.SetAttributes(attribute.String("start", start), attribute.String("end", end))

	total := 0

	eg := errgroup.Group{}
	for _, dates := range batchDates(start, end) {
		s, e := dates[0], dates[1]
		eg.Go(func() error {
			count, err := syncDates(ctx, s, e)
			total += count
			return err
		})
	}
	err := eg.Wait()
	if err != nil {
		handleError(c, err)
		return
	}

	span.SetAttributes(attribute.Int("count", total))

	c.String(http.StatusOK, fmt.Sprintf("%d weights loaded", total))
}

func syncDates(ctx context.Context, start, end string) (int, error) {
	ctx, span := tracer.Start(ctx, "syncDates")
	defer span.End()
	span.SetAttributes(attribute.String("start", start), attribute.String("end", end))

	bw, err := fbc().BodyWeightLogByDateRange(start, end)
	if err != nil {
		return 0, err
	}

	span.SetAttributes(attribute.Int("count", len(bw.Weight)))

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
	ctx, span := tracer.Start(c, "receiveGet")
	defer span.End()
	c.Request.WithContext(ctx)

	verification := os.Getenv("FITBIT_VERIFICATION")
	verify := c.Query("verify")

	if verify != verification {
		c.Status(http.StatusNotFound)
		return
	}

	c.Status(http.StatusNoContent)
}

func rootHandler(c *gin.Context) {
	ctx, span := tracer.Start(c, "display")
	defer span.End()
	c.Request.WithContext(ctx)

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

	c.HTML(http.StatusOK, "weight", gin.H{
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
