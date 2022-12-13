package customer

import (
	"config-service/dbhandler"
	"config-service/mongo"
	"config-service/types"
	"config-service/utils/consts"
	"config-service/utils/log"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/gin-gonic/gin/binding"
	mongoDB "go.mongodb.org/mongo-driver/mongo"
)

func AddRoutes(g *gin.Engine) {
	customer := g.Group("/")

	customer.Use(dbhandler.DBContextMiddleware(consts.CustomersCollection))

	customer.GET("customer", getCustomer)
	customer.POST("customer_tenant", postCustomerTenant)
}

func getCustomer(c *gin.Context) {
	defer log.LogNTraceEnterExit("getCustomer", c)()
	_, customerGUID, err := dbhandler.ReadContext(c)
	if err != nil {
		dbhandler.ResponseInternalServerError(c, "failed to read customer guid from context", err)
		return
	}
	if doc, err := dbhandler.GetDocByGUID[*types.Customer](c, customerGUID); err != nil {
		dbhandler.ResponseInternalServerError(c, "failed to read document", err)
		return
	} else if doc == nil {
		dbhandler.ResponseDocumentNotFound(c)
		return
	} else {
		c.JSON(http.StatusOK, doc)
	}
}

func postCustomerTenant(c *gin.Context) {
	defer log.LogNTraceEnterExit("postCustomerTenant", c)()
	var customer *types.Customer
	if err := c.ShouldBindBodyWith(&customer, binding.JSON); err != nil || customer == nil {
		dbhandler.ResponseFailedToBindJson(c, err)
		return
	}
	if customer.GUID == "" {
		dbhandler.ResponseMissingGUID(c)
		return
	}
	customer.InitNew()
	dbDoc := types.Document[*types.Customer]{
		ID:        customer.GUID,
		Content:   customer,
		Customers: []string{customer.GUID},
	}
	if _, err := mongo.GetWriteCollection(consts.CustomersCollection).InsertOne(c.Request.Context(), dbDoc); err != nil {
		if mongoDB.IsDuplicateKeyError(err) {
			dbhandler.ResponseDuplicateKey(c, consts.GUIDField)
			return
		}
		dbhandler.ResponseInternalServerError(c, "failed to create document", err)
		return
	} else {
		c.JSON(http.StatusCreated, customer)
	}
}
