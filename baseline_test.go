package main

import "testing"

func TestRegressionStoppedContainersSelected(t *testing.T) {
 got := filterContainers([]Container{{Name:"running",StatusCode:103},{Name:"stopped",StatusCode:102}},nil)
 if len(got)!=2 { t.Fatalf("selected %d containers; want running and stopped",len(got)) }
}
