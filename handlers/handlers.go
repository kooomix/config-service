package handlers

import (
	"config-service/db"
	"config-service/types"
	"config-service/utils/consts"
	"config-service/utils/log"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/gin-gonic/gin"
	"k8s.io/utils/strings/slices"
)

/////////////////////////////////////////gin handlers/////////////////////////////////////////

// ////////////////////////////////////////GET///////////////////////////////////////////////

// HandleGetDocWithGUIDInPath - get document of type T by id in path
func HandleGetDocWithGUIDInPath[T types.DocContent](c *gin.Context) {
	defer log.LogNTraceEnterExit("HandleGetDocWithGUIDInPath", c)()
	guid := c.Param(consts.GUIDField)
	if guid == "" {
		ResponseMissingGUID(c)
		return
	}
	if doc, err := db.GetDocByGUID[T](c, guid); err != nil {
		ResponseInternalServerError(c, "failed to read document", err)
		return
	} else {
		docResponse(c, doc)
	}

}

func HandleGet[T types.DocContent](opts *routerOptions[T]) gin.HandlerFunc {
	return func(c *gin.Context) {
		if (!opts.serveGetNamesList || !GetNamesListHandler[T](c, opts.serveGetIncludeGlobalDocs)) &&
			!GetByNameParamHandler[T](c, opts.nameQueryParam) &&
			!GetByScopeParamsHandler[T](c, opts.QueryConfig) {
			HandleGetAll[T](c)
		}
	}
}

// HandleGetAll - get all customer's documents of type T for collection in context
func HandleGetAll[T types.DocContent](c *gin.Context) {
	defer log.LogNTraceEnterExit("HandleGetAll", c)()
	if docs, err := db.GetAllForCustomer[T](c, false); err != nil {
		ResponseInternalServerError(c, "failed to read all documents for customer", err)
		return
	} else {
		docsResponse(c, docs)
	}
}

// HandleGetAll - get all global and customer's documents of type T for collection in context
func HandleGetAllWithGlobals[T types.DocContent](c *gin.Context) {
	defer log.LogNTraceEnterExit("HandleGetAllWithGlobals", c)()
	if docs, err := db.GetAllForCustomer[T](c, true); err != nil {
		ResponseInternalServerError(c, "failed to read all documents for customer", err)
		return
	} else {
		docsResponse(c, docs)
	}
}

// GetNamesList check for "list" query param and return list of names, returns false if not served by this handler
func GetNamesListHandler[T types.DocContent](c *gin.Context, includeGlobals bool) bool {
	if _, list := c.GetQuery(consts.ListParam); list {
		defer log.LogNTraceEnterExit("GetNamesListHandler", c)()
		namesProjection := db.NewProjectionBuilder().Include(consts.NameField).ExcludeID().Get()
		if docNames, err := db.GetAllForCustomerWithProjection[T](c, namesProjection, includeGlobals); err != nil {
			ResponseInternalServerError(c, "failed to read documents", err)
			return true
		} else {
			var names []string
			for _, docContent := range docNames {
				names = append(names, docContent.GetName())
			}
			c.JSON(http.StatusOK, names)
			return true
		}
	}
	return false
}

// HandleGetNameList check for <nameParam> query param and return the element with this name, returns false if not served by this handler
func GetByNameParamHandler[T types.DocContent](c *gin.Context, nameParam string) bool {
	if nameParam == "" {
		return false
	}
	if name := c.Query(nameParam); name != "" {
		defer log.LogNTraceEnterExit("GetByNameParamHandler", c)()
		//get document by name
		if doc, err := db.GetDocByName[T](c, name); err != nil {
			ResponseInternalServerError(c, "failed to read document", err)
			return true
		} else {
			docResponse(c, doc)
			return true
		}
	}
	return false
}

// GetByScopeParams parse scope params and return elements with this scope, returns false if not served by this handler
func GetByScopeParamsHandler[T types.DocContent](c *gin.Context, conf *QueryParamsConfig) bool {
	if conf == nil {
		return false // not served by this handler
	}
	defer log.LogNTraceEnterExit("GetByScopeParamsHandler", c)()

	//keep filter builder per field name
	filterBuilders := map[string]*db.FilterBuilder{}
	getFilterBuilder := func(paramName string) *db.FilterBuilder {
		if filterBuilder, ok := filterBuilders[paramName]; ok {
			return filterBuilder
		}
		filterBuilder := db.NewFilterBuilder()
		filterBuilders[paramName] = filterBuilder
		return filterBuilder
	}

	qParams := c.Request.URL.Query()
	for paramKey, vals := range qParams {
		keys := strings.Split(paramKey, ".")
		//clean whitespaces
		values := slices.Filter([]string{}, vals, func(s string) bool { return s != "" })
		if len(values) == 0 {
			continue
		}
		if len(keys) < 2 {
			keys = []string{conf.DefaultContext, keys[0]}
		} else if len(keys) > 2 {
			keys = []string{keys[0], strings.Join(keys[1:], ".")}
		}
		//escape in case of bad formatted query params
		for i := range values {
			if v, err := url.QueryUnescape(values[i]); err != nil {
				log.LogNTraceError("failed to unescape query param", err, c)
			} else {
				values[i] = v
			}
		}
		//calculate field name
		var field, key = keys[0], keys[1]
		QueryConfig, ok := conf.Params2Query[field]
		if !ok {
			continue
		} else if QueryConfig.IsArray {
			if QueryConfig.PathInArray != "" {
				key = QueryConfig.PathInArray + "." + key
			}
		} else if QueryConfig.FieldName != "" {
			key = QueryConfig.FieldName + "." + key
		}
		//get the field filter builder
		filterBuilder := getFilterBuilder(QueryConfig.FieldName)
		//case of single value
		if len(values) == 1 {
			filterBuilder.WithValue(key, values[0])
		} else { //case of multiple values
			fb := db.NewFilterBuilder()
			for _, v := range values {
				fb.WithValue(key, v)
			}
			filterBuilder.WithFilter(fb.WarpOr().Get())
		}
	}
	//aggregate all filters
	allQueriesFilter := db.NewFilterBuilder()
	for key, filterBuilder := range filterBuilders {
		QueryConfig := conf.Params2Query[key]
		filterBuilder.WrapDupKeysWithOr()
		if QueryConfig.IsArray {
			filterBuilder.WarpElementMatch().WarpWithField(QueryConfig.FieldName)
		}
		allQueriesFilter.WithFilter(filterBuilder.Get())
	}
	if len(allQueriesFilter.Get()) == 0 {
		return false //not served by this handler
	}
	log.LogNTrace(fmt.Sprintf("query params: %v search query %v", qParams, allQueriesFilter.Get()), c)
	if docs, err := db.FindForCustomer[T](c, allQueriesFilter, nil); err != nil {
		ResponseInternalServerError(c, "failed to read documents", err)
		return true
	} else {
		log.LogNTrace(fmt.Sprintf("scope query found %d documents", len(docs)), c)
		docsResponse(c, docs)
		return true
	}
}

// ////////////////////////////////////////POST///////////////////////////////////////////////
// HandlePostDocWithValidation - chains validation and post document handlers
func HandlePostDocWithValidation[T types.DocContent](validators ...MutatorValidator[T]) []gin.HandlerFunc {
	return []gin.HandlerFunc{PostValidationMiddleware(validators...), HandlePostDocFromContext[T]}
}

// HandlePostDocWithUniqueNameValidation - shortcut for HandlePostDocWithValidation(ValidateUniqueValues(NameKeyGetter[T]))
func HandlePostDocWithUniqueNameValidation[T types.DocContent]() []gin.HandlerFunc {
	return []gin.HandlerFunc{PostValidationMiddleware(ValidateUniqueValues(NameKeyGetter[T])), HandlePostDocFromContext[T]}
}

// HandlePostDocFromContext - handles creation of document(s) of type T
func HandlePostDocFromContext[T types.DocContent](c *gin.Context) {
	docs, err := MustGetDocContentFromContext[T](c)
	if err != nil {
		return
	}
	PostDocHandler(c, docs)
}

// PostDoc - helper to put document(s) of type T, custom handler should use this function to do the final POST handling
func PostDocHandler[T types.DocContent](c *gin.Context, docs []T) {
	defer log.LogNTraceEnterExit("PostDocHandler", c)()
	var err error
	if docs, err = db.InsertDocuments(c, docs); err != nil {
		if db.IsDuplicateKeyError(err) {
			ResponseDuplicateKey(c, consts.GUIDField)
			return
		} else {
			ResponseInternalServerError(c, "failed to create document", err)
			return
		}
	} else {
		if len(docs) == 1 {
			c.JSON(http.StatusCreated, docs[0])
		} else {
			c.JSON(http.StatusCreated, docs)
		}
	}
}

func PostDBDocumentHandler[T types.DocContent](c *gin.Context, dbDoc types.Document[T]) {
	if _, err := db.InsertDBDocument(c, dbDoc); err != nil {
		if db.IsDuplicateKeyError(err) {
			ResponseDuplicateKey(c, consts.GUIDField)
			return
		}
		ResponseInternalServerError(c, "failed to create document", err)
		return
	} else {
		c.JSON(http.StatusCreated, dbDoc.Content)
	}
}

// ////////////////////////////////////////PUT///////////////////////////////////////////////

// HandlePutDocWithValidation - chains validation and put document handlers
func HandlePutDocWithValidation[T types.DocContent](validators ...MutatorValidator[T]) []gin.HandlerFunc {
	return []gin.HandlerFunc{PutValidationMiddleware(validators...), HandlePutDocFromContext[T]}
}

// HandlePutDocWithGUIDValidation - shortcut for HandlePutDocWithValidation(ValidateGUIDExistence[T])
func HandlePutDocWithGUIDValidation[T types.DocContent]() []gin.HandlerFunc {
	return []gin.HandlerFunc{PutValidationMiddleware(ValidateGUIDExistence[T]), HandlePutDocFromContext[T]}
}

// HandlePutDocFromContext - handles updates a document of type T
func HandlePutDocFromContext[T types.DocContent](c *gin.Context) {
	docs, err := MustGetDocContentFromContext[T](c)
	if err != nil {
		return
	}
	PutDocHandler(c, docs[0])
}

// PutDoc - helper to put document of type T, custom handler should use this function to do the final PUT handling
func PutDocHandler[T types.DocContent](c *gin.Context, doc T) {
	defer log.LogNTraceEnterExit("PutDocHandler", c)()
	doc.SetUpdatedTime(nil)
	update, err := db.GetUpdateDocCommand(doc, GetCustomPutFields(c), doc.GetReadOnlyFields()...)
	if err != nil {
		if db.IsNoFieldsToUpdateError(err) {
			ResponseBadRequest(c, "no fields to update")
			return
		}
		ResponseInternalServerError(c, "failed to generate update command", err)
		return
	}
	if res, err := db.UpdateDocument[T](c, doc.GetGUID(), update); err != nil {
		ResponseInternalServerError(c, "failed to update document", err)
	} else if res == nil {
		ResponseDocumentNotFound(c)
		return
	} else {
		docsResponse(c, res)
	}
}

// ////////////////////////////////////////DELETE///////////////////////////////////////////////

// HandleDeleteDoc  - delete document by id in path
func HandleDeleteDoc[T types.DocContent](c *gin.Context) {
	guid := c.Param(consts.GUIDField)
	if guid == "" {
		ResponseMissingGUID(c)
		return
	}
	DeleteDocByGUIDHandler[T](c, guid)
}

// HandleDeleteDocByName  - delete document(s) by name in path
func HandleDeleteDocByName[T types.DocContent](nameParam string) gin.HandlerFunc {
	return func(c *gin.Context) {
		defer log.LogNTraceEnterExit("HandleDeleteDocByName", c)()
		names, ok := c.GetQueryArray(nameParam)
		if !ok {
			//try to load from body
			var bodyNames []map[string]string
			if err := c.BindJSON(&bodyNames); err == nil {
				for _, name := range bodyNames {
					names = append(names, name[nameParam])
				}
				ok = true
			}
		}
		names = slices.Filter([]string{}, names, func(s string) bool {
			return s != ""
		})
		if !ok || len(names) == 0 {
			ResponseMissingName(c)
			return
		}
		if len(names) == 1 {
			DeleteDocByNameHandler[T](c, names[0])
		} else {
			BulkDeleteDocByNameHandler[T](c, names)
		}

	}
}

func BulkDeleteDocByNameHandler[T types.DocContent](c *gin.Context, names []string) {
	defer log.LogNTraceEnterExit("BulkDeleteDocByNameHandler", c)()
	if deletedCount, err := db.BulkDeleteByName[T](c, names); err != nil {
	} else if deletedCount == 0 {
		ResponseDocumentNotFound(c)
	} else {
		c.JSON(http.StatusOK, gin.H{"deletedCount": deletedCount})
	}
}

func DeleteDocByGUIDHandler[T types.DocContent](c *gin.Context, guid string) {
	defer log.LogNTraceEnterExit("DeleteDocByGUIDHandler", c)()
	if deletedDoc, err := db.DeleteByGUID[T](c, guid); err != nil {
		ResponseInternalServerError(c, "failed to delete document", err)
	} else if deletedDoc == nil {
		ResponseDocumentNotFound(c)
	} else {
		c.JSON(http.StatusOK, deletedDoc)
	}
}

func DeleteDocByNameHandler[T types.DocContent](c *gin.Context, name string) {
	defer log.LogNTraceEnterExit("DeleteDocByNameHandler", c)()
	if deletedDoc, err := db.DeleteByName[T](c, name); err != nil {
		ResponseInternalServerError(c, "failed to read collection from context", err)
	} else if deletedDoc == nil {
		ResponseDocumentNotFound(c)
	} else {
		c.JSON(http.StatusOK, deletedDoc)
	}
}

// MustGetDocContentFromContext returns document(s) content from context and aborts if not found
func MustGetDocContentFromContext[T types.DocContent](c *gin.Context) ([]T, error) {
	var docs []T
	if iData, ok := c.Get(consts.DocContentKey); ok {
		if doc, ok := iData.(T); ok {
			docs = append(docs, doc)
		} else if docs, ok = iData.([]T); !ok {
			return nil, fmt.Errorf("invalid doc content type")
		}
	} else {
		err := fmt.Errorf("failed to get doc content from context")
		ResponseInternalServerError(c, err.Error(), err)
		return nil, err
	}
	return docs, nil
}

func HandlerAddToArray(requestHandler ContainerHandler) func(c *gin.Context) {
	return func(c *gin.Context) {
		pathToArray, item, valid := requestHandler(c)
		if !valid {
			return
		}
		guid := c.Param(consts.GUIDField)
		if guid == "" {
			ResponseMissingGUID(c)
			return
		}
		if modified, err := db.AddToArray(c, guid, pathToArray, item); err != nil {
			ResponseInternalServerError(c, "failed to add to unsubscribedUsers", err)
			return
		} else {
			c.JSON(http.StatusOK, gin.H{"added": modified})
		}
	}
}

func HandlerRemoveFromArray(requestHandler ContainerHandler) func(c *gin.Context) {
	return func(c *gin.Context) {
		pathToArray, item, valid := requestHandler(c)
		if !valid {
			return
		}
		guid := c.Param(consts.GUIDField)
		if guid == "" {
			ResponseMissingGUID(c)
			return
		}
		if modified, err := db.PullFromArray(c, guid, pathToArray, item); err != nil {
			ResponseInternalServerError(c, "failed to remove from  unsubscribedUsers", err)
			return
		} else {
			c.JSON(http.StatusOK, gin.H{"removed": modified})
		}
	}
}

func HandlerSetField(requestHandler ContainerHandler, set bool) func(c *gin.Context) {
	return func(c *gin.Context) {
		pathToField, value, valid := requestHandler(c)
		if !valid {
			return
		}
		guid := c.Param(consts.GUIDField)
		if guid == "" {
			ResponseMissingGUID(c)
			return
		}
		var update interface{}
		if set {
			update = db.GetUpdateSetFieldCommand(pathToField, value)
		} else { //unset
			update = db.GetUpdateUnsetFieldCommand(pathToField)
		}
		if modified, err := db.UpdateOne(c, guid, update); err != nil {
			ResponseInternalServerError(c, "failed to add to unsubscribedUsers", err)
			return
		} else {
			c.JSON(http.StatusOK, gin.H{"modified": modified})
		}
	}
}
