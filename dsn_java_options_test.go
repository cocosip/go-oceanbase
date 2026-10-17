package oceanbase

import (
	"database/sql"
	"net/url"
	"strings"
	"testing"
	"time"
)

const testConnectTimeoutOption = "connectTimeout"

func TestDSNJavaCanonicalOptions(t *testing.T) {
	// Fixed Connector/J 2.4.14 names; changing this boundary requires an explicit migration decision.
	rejected := strings.Fields(`
        user
        password
        pipe
        localSocket
        sharedMemory
        obProxySocket
        tcpNoDelay
        tcpAbortiveClose
        localSocketAddress
        socketTimeout
        interactiveClient
        dumpQueriesOnException
        useOldAliasMetadataBehavior
        sessionVariables
        createDatabaseIfNotExist
        serverTimezone
        nullCatalogMeansCurrent
        tinyInt1isBit
        yearIsDateType
        useSsl
        useCompression
        allowMultiQueries
        rewriteBatchedStatements
        tcpKeepAlive
        tcpRcvBuf
        tcpSndBuf
        socketFactory
        pinGlobalTxToPhysicalConnection
        trustServerCertificate
        serverSslCert
        useFractionalSeconds
        autoReconnect
        failOnReadOnly
        retriesAllDown
        failoverLoopRetries
        validConnectionTimeout
        loadBalanceBlacklistTimeout
        cachePrepStmts
        prepStmtCacheSize
        prepStmtCacheSqlLimit
        assureReadOnly
        useLegacyDatetimeCode
        maximizeMysqlCompatibility
        useServerPrepStmts
        trustStore
        trustStorePassword
        keyStore
        keyStorePassword
        keyPassword
        enabledSslProtocolSuites
        enabledSslCipherSuites
        continueBatchOnError
        jdbcCompliantTruncation
        cacheCallableStmts
        callableStmtCacheSize
        connectionAttributes
        useBatchMultiSend
        useBatchMultiSendNumber
        log
        profileSql
        maxQuerySizeToLog
        slowQueryThresholdNanos
        passwordCharacterEncoding
        usePipelineAuth
        enablePacketDebug
        disableSslHostnameVerification
        useBulkStmts
        autocommit
        pool
        poolName
        maxPoolSize
        minPoolSize
        maxIdleTime
        poolValidMinDelay
        staticGlobal
        registerJmxPool
        useResetConnection
        allowMasterDownConnection
        galeraAllowedState
        useAffectedRows
        includeInnodbStatusInDeadlockExceptions
        includeThreadDumpInDeadlockExceptions
        useReadAheadInput
        keyStoreType
        trustStoreType
        defaultFetchSize
        useCompatibleMetadata
        blankTableNameMeta
        credentialType
        serverRsaPublicKeyFile
        allowPublicKeyRetrieval
        tlsSocketType
        trackSchema
        supportLobLocator
        useObChecksum
        allowSendParamTypes
        useFormatExceptionMessage
        complexDataCacheSize
        cacheComplexData
        useSqlStringCache
        useServerPsStmtChecksum
        characterEncoding
        useCursorFetch
        supportNameBinding
        socksProxyHost
        socksProxyPort
        connectProxy
        pieceLength
        usePieceData
        useOraclePrepareExecute
        autoDeserialize
        maxBatchTotalParamsNum
        emulateUnsupportedPstmts
        enableQueryTimeouts
        useCursorOffset
        blobSendChunkSize
        tnsnamesPath
        tnsnamesDetectionPeriod
        loadBalanceStrategy
        serverAffinityOrder
        transformedBitIsBoolean
        connectionCollation
        useArrayBinding
        sendConnectionAttributes
        rewriteInsertByMultiQueries
        useLocalXID
        useOceanBaseProtocolV20
        enableFullLinkTrace
        clobberStreamingResults
        maxRows
        zeroDateTimeBehavior
        allowNanAndInf
        defaultConnectionAttributesBanList
        enableOb20Checksum
        ocpAccessInterval
        httpConnectTimeout
        httpReadTimeout
        loadBalanceHandleFailover
        compatibleOjdbcVersion
        compatibleMysqlVersion
        useInformationSchema
        nCharacterEncoding
        emptyStringsConvertToZero
        useLobLocatorV2
        extendOracleResultSetClass
        logTraceTime
        oracleChangeReadOnlyToRepeatableRead
        allowLoadLocalInfile
        encloseParamInParentheses
        remarksReporting
        generateSimpleParameterMetadata
        useProxyUser
        mapDateToTimestamp
        useNewResultSetMetaData
        obDateTypeOptimization
        emulateLocators
        locatorFetchBufferSize
        maxReconnects
    `)
	shared := []struct{ name, value string }{
		{testConnectTimeoutOption, "30s"}, {"servicePrincipalName", "oceanbase/db.example@EXAMPLE.COM"},
	}
	names := make(map[string]bool)
	for _, name := range rejected {
		if names[name] {
			t.Fatalf("duplicate canonical option %s", name)
		}
		names[name] = true
		t.Run(name, func(t *testing.T) { assertMigrationOptionRejected(t, name) })
	}
	for _, tc := range shared {
		if names[tc.name] {
			t.Fatalf("duplicate shared option %s", tc.name)
		}
		names[tc.name] = true
		t.Run(tc.name, func(t *testing.T) {
			dsn := "oceanbase://app@localhost/demo?" + url.Values{tc.name: {tc.value}}.Encode()
			cfg, err := ParseDSN(dsn)
			if err != nil {
				t.Fatal(err)
			}
			if tc.name == testConnectTimeoutOption && cfg.ConnectTimeout != 30*time.Second || tc.name == "servicePrincipalName" && cfg.Auth.ServicePrincipalName != tc.value {
				t.Fatal("shared name lost Go semantics")
			}
			db, err := sql.Open("oceanbase", dsn)
			if err != nil {
				t.Fatal(err)
			}
			if err = db.Close(); err != nil {
				t.Fatal(err)
			}
		})
	}
	if len(names) != 160 {
		t.Fatalf("canonical inventory changed: %d", len(names))
	}
}
