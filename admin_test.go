package main

import (
	"config-service/db"
	"config-service/db/mongo"
	"config-service/types"
	"config-service/utils/consts"
	"context"
	"fmt"
	"net/http"
	"sort"

	_ "embed"

	"github.com/armosec/armoapi-go/armotypes"
	"github.com/google/go-cmp/cmp"
)

func (suite *MainTestSuite) TestAdminAndUsers() {
	const (
		user1 = "user1-guid"
		user2 = "f5f360bb-c233-4c33-a9af-5692e7795d61"
		user3 = "2ce5daf4-e28d-4e6e-a239-03fda048070b"
		admin = "admin-user-guid"
	)

	users := []string{user1, user2, user3}

	clusters, clustersNames := loadJson[*types.Cluster](clustersJson)
	frameworks, frameworksNames := loadJson[*types.Framework](frameworksJson)
	posturePolices, policiesNames := loadJson[*types.PostureExceptionPolicy](posturePoliciesJson)
	vulnerabilityPolicies, vulnerabilityNames := loadJson[*types.VulnerabilityExceptionPolicy](vulnerabilityPoliciesJson)
	repositories, repositoriesNames := loadJson[*types.Repository](repositoriesJson)
	registryCronJobs, registryCronJobNames := loadJson[*types.RegistryCronJob](registryCronJobJson)

	populateUser := func(userGUID string) {
		suite.login(userGUID)
		testBulkPostDocs(suite, consts.ClusterPath, clusters, newClusterCompareFilter)
		testBulkPostDocs(suite, consts.FrameworkPath, frameworks, fwCmpFilter)
		testBulkPostDocs(suite, consts.PostureExceptionPolicyPath, posturePolices, commonCmpFilter)
		testBulkPostDocs(suite, consts.VulnerabilityExceptionPolicyPath, vulnerabilityPolicies, commonCmpFilter)
		testBulkPostDocs(suite, consts.RepositoryPath, repositories, repoCompareFilter)
		testBulkPostDocs(suite, consts.RegistryCronJobPath, registryCronJobs, rCmpFilter)

		customer := &types.Customer{
			PortalBase: armotypes.PortalBase{
				Name: userGUID,
				GUID: userGUID,
			},
		}
		testPostDoc(suite, consts.TenantPath, customer, customerCompareFilter)
	}

	verifyUserData := func(userGUID string) {
		suite.login(userGUID)
		testGetNameList(suite, consts.ClusterPath, clustersNames)
		testGetNameList(suite, consts.FrameworkPath, frameworksNames)
		testGetNameList(suite, consts.PostureExceptionPolicyPath, policiesNames)
		testGetNameList(suite, consts.VulnerabilityExceptionPolicyPath, vulnerabilityNames)
		testGetNameList(suite, consts.RepositoryPath, repositoriesNames)
		testGetNameList(suite, consts.RegistryCronJobPath, registryCronJobNames)

		customer := &types.Customer{
			PortalBase: armotypes.PortalBase{
				Name: userGUID,
				GUID: userGUID,
			},
		}

		testGetDoc(suite, "/customer", customer, customerCompareFilter)
	}

	verifyUserDataDeleted := func(userGUID string) {
		suite.login(userGUID)
		testGetNameList(suite, consts.ClusterPath, nil)
		testGetNameList(suite, consts.FrameworkPath, nil)
		testGetNameList(suite, consts.PostureExceptionPolicyPath, nil)
		testGetNameList(suite, consts.VulnerabilityExceptionPolicyPath, nil)
		testGetNameList(suite, consts.RepositoryPath, nil)
		testGetNameList(suite, consts.RegistryCronJobPath, nil)
		testBadRequest(suite, http.MethodGet, consts.CustomerPath, errorDocumentNotFound, nil, http.StatusNotFound)

	}

	for _, userGUID := range users {
		populateUser(userGUID)
		verifyUserData(userGUID)
	}
	//login as admin
	suite.loginAsAdmin("a-admin-guid")
	//delete users2 and users3 data
	deleteUsersUrls := fmt.Sprintf("%s/customers?%s=%s&%s=%s", consts.AdminPath, consts.CustomersParam, user2, consts.CustomersParam, user3)
	type deletedResponse struct {
		Deleted int64 `json:"deleted"`
	}
	w := suite.doRequest(http.MethodDelete, deleteUsersUrls, nil)
	suite.Equal(http.StatusOK, w.Code)
	response, err := decodeResponse[*deletedResponse](w)
	if err != nil {
		suite.FailNow(err.Error())
	}
	//expect 2 customers doc and all what they have
	deletedCount := 2 * (1 + len(clusters) + len(frameworks) + len(posturePolices) + len(vulnerabilityPolicies) + len(repositories) + len(registryCronJobs))
	suite.Equal(int64(deletedCount), response.Deleted)
	//verify user1 data is still there
	verifyUserData(user1)
	//verify user2 and user3 data is gone
	for _, userGUID := range users[1:] {
		verifyUserDataDeleted(userGUID)
	}

	//make sure regular user can't use admin api
	suite.login(user1)
	testBadRequest(suite, http.MethodDelete, deleteUsersUrls, errorNotAdminUser, nil, http.StatusUnauthorized)

	//populate user2 again
	suite.login(user2)
	populateUser(user2)
	verifyUserData(user2)
	//test customer delete they own data with  DELETE /customer api
	w = suite.doRequest(http.MethodDelete, consts.CustomerPath, nil)
	suite.Equal(http.StatusOK, w.Code)
	response, err = decodeResponse[*deletedResponse](w)
	if err != nil {
		suite.FailNow(err.Error())
	}

	deletedCount = 1 + len(clusters) + len(frameworks) + len(posturePolices) + len(vulnerabilityPolicies) + len(repositories) + len(registryCronJobs)
	suite.Equal(int64(deletedCount), response.Deleted)
	//verify user2 data is gone
	verifyUserDataDeleted(user2)
	//verify user1 data is still there
	verifyUserData(user1)

	//login as admin from the config admins list
	suite.login(admin)
	//delete user1 data
	deleteUsersUrls = fmt.Sprintf("%s/customers?%s=%s", consts.AdminPath, consts.CustomersParam, user1)
	w = suite.doRequest(http.MethodDelete, deleteUsersUrls, nil)
	suite.Equal(http.StatusOK, w.Code)
	response, err = decodeResponse[*deletedResponse](w)
	if err != nil {
		suite.FailNow(err.Error())
	}

	suite.Equal(int64(deletedCount), response.Deleted)
	//verify user1 data is gone
	verifyUserDataDeleted(user1)

	//test bad delete customers request with no users
	suite.loginAsAdmin("other-admin-guid")
	deleteUsersUrls = fmt.Sprintf("%s/customers", consts.AdminPath)
	testBadRequest(suite, http.MethodDelete, deleteUsersUrls, errorMissingQueryParams(consts.CustomersParam), nil, http.StatusBadRequest)

}

//go:embed test_data/active_users/users.json
var activeUsersBytes []byte

//go:embed test_data/active_users/user1Clusters.json
var user1ClustersBytes []byte

//go:embed test_data/active_users/user2Clusters.json
var user2ClustersBytes []byte

//go:embed test_data/active_users/user3Clusters.json
var user3ClustersBytes []byte

func (suite *MainTestSuite) TestAdminActiveUsers() {
	users, _ := loadJson[*types.Customer](activeUsersBytes)
	clusters1, _ := loadJson[*types.Cluster](user1ClustersBytes)
	clusters2, _ := loadJson[*types.Cluster](user2ClustersBytes)
	clusters3, _ := loadJson[*types.Cluster](user3ClustersBytes)
	clusters := [][]*types.Cluster{clusters1, clusters2, clusters3}

	_, err := mongo.GetWriteCollection(consts.ClustersCollection).DeleteMany(context.Background(), struct{}{})
	suite.NoError(err, "can't delete clusters collection")

	for i, user := range users {
		testPostDoc(suite, consts.TenantPath, user, customerCompareFilter)
		suite.login(user.GUID)
		for _, cluster := range clusters[i] {
			testPostDoc(suite, consts.ClusterPath, cluster, newClusterCompareFilter)
		}
	}

	testActive := func(from, to string, limit, skip int, expectedUsers ...*types.Customer) *db.AggResult[*types.Customer] {
		suite.loginAsAdmin("admin-guid")
		activeUsersUrl := fmt.Sprintf("%s/activeCustomers?%s=%s&%s=%s", consts.AdminPath, consts.FromDateParam, from, consts.ToDateParam, to)
		if limit > 0 {
			activeUsersUrl = fmt.Sprintf("%s&%s=%d", activeUsersUrl, consts.LimitParam, limit)
		}
		if skip > 0 {
			activeUsersUrl = fmt.Sprintf("%s&%s=%d", activeUsersUrl, consts.SkipParam, skip)
		}
		w := suite.doRequest(http.MethodGet, activeUsersUrl, nil)
		suite.Equal(http.StatusOK, w.Code)
		response, err := decodeResponse[*db.AggResult[*types.Customer]](w)
		if err != nil {
			suite.FailNow(err.Error())
		}
		suite.Equal(len(expectedUsers), len(response.Results))
		if limit > 0 {
			suite.Equal(limit, response.Metadata.Limit)
		} else {
			suite.Equal(1000, response.Metadata.Limit)
		}
		sort.Slice(response.Results, func(i, j int) bool {
			return response.Results[i].GUID < response.Results[j].GUID
		})
		if expectedUsers == nil {
			expectedUsers = []*types.Customer{}
		}
		diff := cmp.Diff(expectedUsers, response.Results, customerCompareFilter)
		suite.Empty(diff, "active users response is not as expected")
		return response

	}
	testActive("2022-01-01T20:00:00Z", "2024-01-01T20:00:00Z", 0, 0, users...)
	testActive("2022-11-15T11:13:30Z", "2022-11-15T11:13:32Z", 0, 0, users[2])
	testActive("2023-01-01T11:13:32Z", "2023-01-16T11:13:32Z", 0, 0, users[0], users[2])
	testActive("2023-01-25T11:13:32Z", "2023-02-01T11:13:32Z", 0, 0, users[1])
	testActive("2026-01-25T11:13:32Z", "2027-02-01T11:13:32Z", 0, 0)

	res := testActive("2022-01-01T20:00:00Z", "2024-01-01T20:00:00Z", 1, 0, users[0])
	suite.Equal(res.Metadata.Total, 3)
	suite.Equal(res.Metadata.NextSkip, 1)
	res = testActive("2022-01-01T20:00:00Z", "2024-01-01T20:00:00Z", 1, 1, users[1])
	suite.Equal(res.Metadata.Total, 3)
	suite.Equal(res.Metadata.NextSkip, 2)
	res = testActive("2022-01-01T20:00:00Z", "2024-01-01T20:00:00Z", 1, 2, users[2])
	suite.Equal(res.Metadata.Total, 3)
	suite.Equal(res.Metadata.NextSkip, 0)

	missingParamUrl := fmt.Sprintf("%s/activeCustomers?%s=%s", consts.AdminPath, consts.FromDateParam, "2024-01-01T20:00:00Z")
	testBadRequest(suite, http.MethodGet, missingParamUrl, errorMissingQueryParams(consts.ToDateParam), nil, http.StatusBadRequest)
	missingParamUrl = fmt.Sprintf("%s/activeCustomers?%s=%s", consts.AdminPath, consts.ToDateParam, "2024-01-01T20:00:00Z")
	testBadRequest(suite, http.MethodGet, missingParamUrl, errorMissingQueryParams(consts.FromDateParam), nil, http.StatusBadRequest)
	badTimeParamUrl := fmt.Sprintf("%s/activeCustomers?%s=%s&%s=%s", consts.AdminPath, consts.FromDateParam, "2024-01-01T20:00:00Z", consts.ToDateParam, "some-bad-time")
	testBadRequest(suite, http.MethodGet, badTimeParamUrl, errorBadTimeParam(consts.ToDateParam), nil, http.StatusBadRequest)
	badTimeParamUrl = fmt.Sprintf("%s/activeCustomers?%s=%s&%s=%s", consts.AdminPath, consts.ToDateParam, "2024-01-01T20:00:00Z", consts.FromDateParam, "some-bad-time")
	testBadRequest(suite, http.MethodGet, badTimeParamUrl, errorBadTimeParam(consts.FromDateParam), nil, http.StatusBadRequest)
	badParamTypeUrl := fmt.Sprintf("%s/activeCustomers?%s=%s&%s=%s&%s=%s", consts.AdminPath, consts.FromDateParam, "2024-01-01T20:00:00Z", consts.ToDateParam, "2024-01-01T20:00:00Z", consts.LimitParam, "some-bad-limit")
	testBadRequest(suite, http.MethodGet, badParamTypeUrl, errorParamType(consts.LimitParam, "number"), nil, http.StatusBadRequest)
	badParamTypeUrl = fmt.Sprintf("%s/activeCustomers?%s=%s&%s=%s&%s=%s", consts.AdminPath, consts.FromDateParam, "2024-01-01T20:00:00Z", consts.ToDateParam, "2024-01-01T20:00:00Z", consts.SkipParam, "some-bad-limit")
	testBadRequest(suite, http.MethodGet, badParamTypeUrl, errorParamType(consts.SkipParam, "number"), nil, http.StatusBadRequest)
}
