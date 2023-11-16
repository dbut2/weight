#!/bin/zsh

gcloud secrets versions access latest --project dbut-0 --secret fitbit-client-secret > secrets/fitbit-client-secret
gcloud secrets versions access latest --project dbut-0 --secret fitbit-token > secrets/fitbit-token
gcloud secrets versions access latest --project dbut-0 --secret fitbit-verification > secrets/fitbit-verification
