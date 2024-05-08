package db

import (
	"config-service/db/mongo"
	"config-service/types"
	"config-service/utils"
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"strings"

	"config-service/utils/log"
	"text/template"

	"github.com/armosec/armoapi-go/armotypes"
	mongoDB "go.mongodb.org/mongo-driver/mongo"

	"go.mongodb.org/mongo-driver/bson"
)

const MaxAggregationLimit = 10000

type preDefinedQuery string

const (
	CustomersWithScansBetweenDates preDefinedQuery = "customersWithScansBetweenDates"
)

var rootTemplate = template.New("root")

//go:embed predefined_queries/customersWithScansBetweenDates.txt
var CustomersWithScansBetweenDatesBytes string

func Init() {
	t := rootTemplate.New(string(CustomersWithScansBetweenDates))
	template.Must(t.Parse(CustomersWithScansBetweenDatesBytes))
}

type Metadata struct {
	Total    int `json:"total" bson:"total"`
	Limit    int `json:"limit" bson:"limit"`
	NextSkip int `json:"nextSkip" bson:"nextSkip"`
}

type AggResult[T any] struct {
	Metadata Metadata `json:"metadata" bson:"metadata"`
	Results  []T      `json:"results" bson:"results"`
}

type aggResponse[T any] struct {
	Metadata []Metadata `json:"metadata" bson:"metadata"`
	Results  []T        `json:"results" bson:"results"`
}

func AggregateWithTemplate[T any](ctx context.Context, limit, cursor int, collection string, queryTemplateName preDefinedQuery, templateArgs map[string]interface{}) (*AggResult[T], error) {
	msg := fmt.Sprintf("AggregateWithTemplate collection %s queryTemplateName %s  templateArgs %v", collection, queryTemplateName, templateArgs)
	log.LogNTraceEnterExit(msg, ctx)()
	if templateArgs == nil {
		templateArgs = map[string]interface{}{}
	}
	templateArgs["skip"] = cursor
	if limit == 0 || limit > MaxAggregationLimit {
		limit = MaxAggregationLimit
	}
	templateArgs["limit"] = limit
	buf := strings.Builder{}
	if err := rootTemplate.ExecuteTemplate(&buf, string(queryTemplateName), templateArgs); err != nil {
		log.LogNTraceError("failed to execute template", err, ctx)
		return nil, err
	}
	var pipeline []bson.M
	if err := json.Unmarshal([]byte(buf.String()), &pipeline); err != nil {
		log.LogNTraceError("failed to unmarshal template", err, ctx)
		return nil, err
	}
	dbCursor, err := mongo.GetReadCollection(collection).Aggregate(ctx, pipeline)
	if err != nil {
		log.LogNTraceError("failed aggregate", err, ctx)
		return nil, err
	}
	defer dbCursor.Close(ctx)

	resultsSlice := []aggResponse[T]{}
	if err := dbCursor.All(ctx, &resultsSlice); err != nil {
		log.LogNTraceError("failed to decode results", err, ctx)
		return nil, err
	}
	results := AggResult[T]{}
	if len(resultsSlice) == 0 {
		return &results, nil
	}
	if len(resultsSlice[0].Metadata) != 0 {
		results.Metadata = resultsSlice[0].Metadata[0]
	}
	results.Metadata.Limit = limit
	results.Results = resultsSlice[0].Results
	if cursor+len(results.Results) < results.Metadata.Total {
		results.Metadata.NextSkip = cursor + len(results.Results)
	}

	return &results, nil
}

func uniqueValuePipeline(fields []string, match bson.D, skip, limit int64, schemaInfo types.SchemaInfo) mongoDB.Pipeline {
	pipeline := mongoDB.Pipeline{
		{{Key: "$match", Value: match}},
	}
	handledArrays := map[string]bool{}
	for _, field := range fields {
		isArray, arrayPath, _ := schemaInfo.GetArrayDetails(field)
		if isArray && !handledArrays[arrayPath] {
			handledArrays[arrayPath] = true
			pipeline = append(pipeline,
				bson.D{{Key: "$unwind", Value: "$" + arrayPath}},
				//after unwind we need to match again
				bson.D{{Key: "$match", Value: matchFiltersForUnwind(arrayPath, match)}},
			)
		}
	}
	var filedRef interface{}
	if len(fields) == 1 {
		filedRef = "$" + fields[0]
	} else {
		setM := bson.M{}
		groupM := bson.M{}
		for _, field := range fields {
			cleanField := strings.ReplaceAll(field, ".", "_")
			setM[cleanField] = "$" + field
			groupM[cleanField] = "$" + cleanField
		}
		pipeline = append(pipeline, bson.D{{Key: "$set", Value: setM}})
		filedRef = groupM
	}
	pipeline = append(pipeline,
		bson.D{{Key: "$group", Value: bson.D{
			{Key: "_id", Value: filedRef},
			{Key: "count", Value: bson.D{{Key: "$sum", Value: 1}}},
		}}},
		bson.D{{Key: "$sort", Value: bson.D{
			{Key: "_id", Value: 1},
		}}},
	)
	if skip > 0 {
		pipeline = append(pipeline, bson.D{{Key: "$skip", Value: skip}})
	}
	pipeline = append(pipeline,
		bson.D{{Key: "$limit", Value: limit}},
		bson.D{{Key: "$group", Value: bson.D{
			{Key: "_id", Value: nil},
			{Key: "values", Value: bson.D{{Key: "$push", Value: "$_id"}}},
			{Key: "count", Value: bson.D{{Key: "$push", Value: bson.D{{Key: "key", Value: "$_id"}, {Key: "count", Value: "$count"}}}}},
		}}},
		bson.D{{Key: "$project", Value: bson.D{
			{Key: "_id", Value: 0},
			{Key: "values", Value: 1},
			{Key: "count", Value: 1},
		}}},
	)
	return pipeline
}

func addUniqueValuesResult(aggregatedResults *armotypes.UniqueValuesResponseV2, field string, result aggregateResult) {
	aggregatedResults.Fields[field] = make([]string, len(result.Values))
	for i, value := range result.Values {
		aggregatedResults.Fields[field][i] = utils.Interface2String(value)
	}
	aggregatedResults.FieldsCount[field] = []armotypes.UniqueValuesResponseFieldsCount{}
	for _, count := range result.Count {
		aggregatedResults.FieldsCount[field] = append(aggregatedResults.FieldsCount[field], armotypes.UniqueValuesResponseFieldsCount{
			Field: utils.Interface2String(count.Key),
			Count: count.Count,
		})
	}
}

// matchFiltersForUnwind adjusts filters for use after an $unwind array stage.
func matchFiltersForUnwind(arrayName string, match bson.D) bson.D {
	var unwindMatch bson.D
	for _, elem := range match {
		if elem.Key == arrayName && isBsonD(elem.Value) {
			unwindMatch = append(unwindMatch, transformArrayConditionsForUnwind(arrayName, elem.Value.(bson.D))...)
		} else {
			unwindMatch = append(unwindMatch, elem)
		}
	}
	return unwindMatch
}

func isBsonD(value interface{}) bool {
	if _, ok := value.(bson.D); ok {
		return true
	}
	return false
}

// transformElemMatchForUnwind transforms an $elemMatch condition to be applicable after $unwind.
func transformArrayConditionsForUnwind(arrayName string, filter bson.D) bson.D {
	var transformed bson.D
	for _, cond := range filter {
		if cond.Key == "$elemMatch" {
			transformed = append(transformed, processElemMatchConditions(arrayName, cond.Value.(bson.D))...)
		} else {
			transformed = append(transformed, cond)
		}
	}
	return transformed
}

// processElemMatchConditions handles conditions inside $elemMatch after $unwind.
func processElemMatchConditions(arrayName string, conditions bson.D) bson.D {
	var transformed bson.D
	for _, cond := range conditions {
		if strings.HasPrefix(cond.Key, "$") {
			transformed = append(transformed, bson.E{Key: cond.Key, Value: addArrayPath2Keys(arrayName, cond.Value)})
		} else {
			// Direct field conditions
			transformed = append(transformed, bson.E{Key: arrayName + "." + cond.Key, Value: cond.Value})
		}
	}
	return transformed
}

func addArrayPath2Keys(arrayPath string, filterVal interface{}) interface{} {
	switch v := filterVal.(type) {
	case bson.D:
		var newFilter bson.D
		for _, elem := range v {
			newFilter = append(newFilter, bson.E{Key: arrayPath + "." + elem.Key, Value: elem.Value})
		}
		return newFilter
	case bson.A:
		var newArray bson.A
		for _, elem := range v {
			newArray = append(newArray, addArrayPath2Keys(arrayPath, elem))
		}
		return newArray
	case bson.M:
		newFilter := make(bson.M)
		for key, value := range v {
			newFilter[arrayPath+"."+key] = value
		}
		return newFilter
	case []bson.M:
		var newArray []bson.M
		for _, elem := range v {
			newArray = append(newArray, addArrayPath2Keys(arrayPath, elem).(bson.M))
		}
		return newArray
	default:
		return filterVal
	}
}
