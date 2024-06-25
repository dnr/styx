package main

import (
	"context"
	"strings"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	ssm "github.com/aws/aws-sdk-go-v2/service/ssm"
	"github.com/nix-community/go-nix/pkg/narinfo/signature"
)

func loadKeysFromSsm(names []string) ([]signature.SecretKey, error) {
	awscfg, err := awsconfig.LoadDefaultConfig(context.Background(), awsconfig.WithEC2IMDSRegion())
	if err != nil {
		return nil, err
	}
	ssmcli := ssm.NewFromConfig(awscfg)
	decrypt := true
	keys := make([]signature.SecretKey, len(names))
	for i, name := range names {
		if out, err := ssmcli.GetParameter(context.Background(), &ssm.GetParameterInput{
			Name:           &name,
			WithDecryption: &decrypt,
		}); err != nil {
			return nil, err
		} else if keys[i], err = signature.LoadSecretKey(strings.TrimSpace(*out.Parameter.Value)); err != nil {
			return nil, err
		}
	}
	return keys, nil
}
