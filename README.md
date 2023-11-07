# Go Fitbit Weight Tracker

This Go application is a web service that integrates with the Fitbit API to track and log weight data. It allows users to synchronize their weight information from their Fitbit account and stores it in Google Cloud Datastore. It also implements OpenTelemetry for tracing and Secret Manager for secure storage of tokens.

## Features

- **Fitbit API Integration**: Synchronize weight data from Fitbit.
- **Google Cloud Datastore**: Store and retrieve weight logs.
- **Secret Management**: Use Google Cloud Secret Manager to handle Fitbit tokens securely.
- **OpenTelemetry Tracing**: Trace operations within the application using Google Cloud's operations suite.
- **Batch Operations**: Handle batch processing of weight data over a specified date range.
- **Webhooks**: Accept data from Fitbit subscriptions via webhooks.
- **Templating**: Serve web pages using Go's `html/template` package.

## Requirements

- Go 1.21 or higher
- Access to Google Cloud Datastore
- Access to Google Cloud Secret Manager
- A Fitbit Developer account and application set up for API access
- Configured environment variables for Fitbit API credentials and Google Cloud Project ID

## Setup

Before running the application, ensure that all necessary environment variables are set:

```shell
export PROJECT_ID='your-gcp-project-id'
export FITBIT_CLIENT='your-fitbit-client-id'
export FITBIT_SECRET='your-fitbit-secret'
export FITBIT_REDIRECT='your-fitbit-redirect-url'
export FITBIT_TOKEN_SECRET='path-to-your-fitbit-token-secret-in-secret-manager'
export PORT='8080' # Optional: default is 8080
```

## Installation

Clone the repository to your local machine and install dependencies:

```shell
git clone https://github.com/dbut2/weight.git
cd weight
go mod tidy
```

## Running the Application

To start the server, use the following command:

```shell
go run .
```

The application will start listening for incoming requests on the specified `PORT`.

## Endpoints

- `POST /receive`: Endpoint to receive subscription updates from Fitbit.
- `GET /receive`: Endpoint for Fitbit subscription verification.
- `GET /batch`: Endpoint to manually trigger a batch processing of weights between two dates. 
- `GET /`: Main page displaying the most recent weight data.

## Security

Please ensure that you do not expose sensitive credentials in your environment variables. Always use a secure method to set and store these values.

## Contributing

We welcome contributions to this project. Please open an issue or a pull request if you'd like to propose changes or improvements.
