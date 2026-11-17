package jobstore

import (
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

// RunIDIndex is the GSI tools/collect.py queries to export one run's job records.
const RunIDIndex = "run_id-index"

// CreateTableInput is the jobs table definition. local/init.sh and terraform/modules/dynamodb
// must define the same key, index and projection.
func CreateTableInput(table string) *dynamodb.CreateTableInput {
	return &dynamodb.CreateTableInput{
		TableName:   aws.String(table),
		BillingMode: types.BillingModePayPerRequest,
		AttributeDefinitions: []types.AttributeDefinition{
			{AttributeName: aws.String("job_id"), AttributeType: types.ScalarAttributeTypeS},
			{AttributeName: aws.String("run_id"), AttributeType: types.ScalarAttributeTypeS},
		},
		KeySchema: []types.KeySchemaElement{
			{AttributeName: aws.String("job_id"), KeyType: types.KeyTypeHash},
		},
		GlobalSecondaryIndexes: []types.GlobalSecondaryIndex{{
			IndexName:  aws.String(RunIDIndex),
			KeySchema:  []types.KeySchemaElement{{AttributeName: aws.String("run_id"), KeyType: types.KeyTypeHash}},
			Projection: &types.Projection{ProjectionType: types.ProjectionTypeAll},
		}},
	}
}
