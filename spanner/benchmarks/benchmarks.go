// Copyright 2025 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     https://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"slices"
	"strconv"
	"time"

	"cloud.google.com/go/spanner"
	"google.golang.org/api/iterator"
)

const (
	selectQuery  = "SELECT ID from EMPLOYEES WHERE ID = @p1"
	totalRecords = 100000
	tableName    = "EMPLOYEES"
)

type transactionType string

const (
	query transactionType = "query"
	read                  = "read"
)

func main() {
	ctx := context.Background()

	project := os.Getenv("SPANNER_CLIENT_BENCHMARK_GOOGLE_CLOUD_PROJECT")
	instance := os.Getenv("SPANNER_CLIENT_BENCHMARK_SPANNER_INSTANCE")
	database := os.Getenv("SPANNER_CLIENT_BENCHMARK_SPANNER_DATABASE")

	if project == "" || instance == "" || database == "" {
		fmt.Println(`You must set all the environment variables SPANNER_CLIENT_BENCHMARK_GOOGLE_CLOUD_PROJECT, 
			SPANNER_CLIENT_BENCHMARK_SPANNER_INSTANCE and SPANNER_CLIENT_BENCHMARK_SPANNER_DATABASE`)
		return
	}

	if len(os.Args) < 6 {
		fmt.Println("Please set warm up time, execution time, wait between requests and staleness in the command line arguments")
		return
	}

	warmupTime, _ := strconv.ParseInt(os.Args[1], 10, 8)          // in minutes
	executionTime, _ := strconv.ParseInt(os.Args[2], 10, 8)       // in minutes
	waitBetweenRequests, _ := strconv.ParseInt(os.Args[3], 10, 8) // in milliseconds
	staleness, _ := strconv.ParseInt(os.Args[4], 10, 8)           // in seconds
	parsedTransactionType, err := parseTransactionType(os.Args[5])
	if err != nil {
		fmt.Println(err)
		return
	}

	db := fmt.Sprintf("projects/%v/instances/%v/databases/%v", project, instance, database)

	fmt.Printf("Running benchmark on %v\nWarm up time: %v mins\nExecution Time: %v mins\nWait Between Requests: %v ms\nStaleness: %v\n", db, warmupTime, executionTime, waitBetweenRequests, staleness)

	client, err := spanner.NewClientWithConfig(ctx, db, spanner.ClientConfig{})
	if err != nil {
		return
	}
	defer client.Close()

	err = warmUp(ctx, client, warmupTime, staleness, parsedTransactionType)
	if err != nil {
		fmt.Println(err)
		return
	}

	latencies, err := runBenchmark(ctx, client, executionTime, staleness, waitBetweenRequests, parsedTransactionType)
	if err != nil {
		fmt.Println(err)
		return
	}

	slices.Sort(latencies)

	fmt.Printf("\nResults\np50 %v\np95 %v\np99 %v\n", percentiles(0.5, latencies),
		percentiles(0.95, latencies), percentiles(0.99, latencies))
}

func warmUp(ctx context.Context, client *spanner.Client, warmupTime int64, staleness int64, transactionType transactionType) error {
	endTime := time.Now().Local().Add(time.Minute * time.Duration(warmupTime))

	go runTimer(endTime, "Remaining warmup time")
	for {
		if time.Now().Local().After(endTime) {
			break
		}
		_, err := execute(ctx, transactionType, client, staleness)
		if err != nil {
			fmt.Println(err)
			return err
		}
	}
	return nil
}

func runBenchmark(ctx context.Context, client *spanner.Client, executionTime int64, staleness int64, waitBetweenRequests int64, transactionType transactionType) ([]int64, error) {
	endTime := time.Now().Local().Add(time.Minute * time.Duration(executionTime))

	go runTimer(endTime, "Remaining operation time")
	var durations []int64
	for {
		if time.Now().Local().After(endTime) {
			break
		}
		duration, err := execute(ctx, transactionType, client, staleness)
		if err != nil {
			fmt.Println(err)
			return make([]int64, 0), err
		}
		durations = append(durations, duration)
		time.Sleep(time.Millisecond * getRandomWaitTime(waitBetweenRequests))
	}

	return durations, nil
}

func execute(ctx context.Context, transactionType transactionType, client *spanner.Client, staleness int64) (int64, error) {
	switch transactionType {
	case query:
		return executeQuery(ctx, client, staleness)
	case read:
		return executeRead(ctx, client, staleness)
	default:
		return 0, errors.New("invalid transaction type")
	}
}

func executeQuery(ctx context.Context, client *spanner.Client, staleness int64) (int64, error) {
	start := time.Now()

	iter := client.Single().WithTimestampBound(spanner.ExactStaleness(time.Second*time.Duration(staleness))).Query(ctx, spanner.Statement{SQL: selectQuery, Params: map[string]interface{}{
		"p1": generateUniqueID(),
	}})
	for {
		row, err := iter.Next()
		if errors.Is(err, iterator.Done) {
			break
		}
		if err != nil {
			return time.Duration(0).Microseconds(), err
		}

		var id int64
		if err := row.Columns(&id); err != nil {
			return time.Duration(0).Microseconds(), err
		}
	}

	return time.Since(start).Microseconds(), nil
}

func executeRead(ctx context.Context, client *spanner.Client, staleness int64) (int64, error) {
	start := time.Now()

	iter := client.Single().WithTimestampBound(spanner.ExactStaleness(time.Second*time.Duration(staleness))).Read(ctx, tableName, spanner.Key{generateUniqueID()}, []string{"ID"})
	for {
		row, err := iter.Next()
		if errors.Is(err, iterator.Done) {
			break
		}
		if err != nil {
			return time.Duration(0).Microseconds(), err
		}

		var id int64
		if err := row.Columns(&id); err != nil {
			return time.Duration(0).Microseconds(), err
		}
	}

	return time.Since(start).Microseconds(), nil
}

func runTimer(endTime time.Time, text string) {
	for {
		var t time.Time
		t = t.Add(endTime.Sub(time.Now()))
		fmt.Printf("\r%v %v", text, t.Format(time.TimeOnly))
		time.Sleep(time.Second)
		if time.Now().Local().After(endTime) {
			break
		}
	}
}

func parseTransactionType(s string) (transactionType, error) {
	switch s {
	case "READ":
		return read, nil
	case "QUERY":
		return query, nil
	default:
		return query, errors.New("invalid transaction type")
	}
}

func percentiles(percentile float32, latencies []int64) any {
	rank := (percentile * float32(len(latencies)-1)) + 1
	return latencies[uint(rank)]
}

func generateUniqueID() int64 {
	return rand.Int64N(totalRecords) + 1
}

func getRandomWaitTime(waitTime int64) time.Duration {
	return time.Duration(rand.Int64N(2*waitTime-1) + 1)
}
