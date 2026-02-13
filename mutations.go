package sfveritas

// GraphQL mutation strings matching the exact schema expected by the Sailfish backend.

const mutationIdentifyServiceDetails = `mutation IdentifyServiceDetails(
  $apiKey: String!,
  $timestampMs: String!,
  $serviceUuid: String!,
  $serviceIdentifier: String,
  $serviceVersion: String,
  $serviceAdditionalMetadata: JSON,
  $library: String!,
  $version: String!,
  $infrastructureType: String,
  $infrastructureDetails: JSON,
  $setupInterceptorsFilePath: String,
  $setupInterceptorsLineNumber: Int,
  $gitSha: String,
  $gitOrg: String,
  $gitRepo: String,
  $gitProvider: String,
  $serviceDisplayName: String
) {
  identifyServiceDetails(
    apiKey: $apiKey,
    timestampMs: $timestampMs,
    serviceUuid: $serviceUuid,
    serviceIdentifier: $serviceIdentifier,
    serviceVersion: $serviceVersion,
    serviceAdditionalMetadata: $serviceAdditionalMetadata,
    library: $library,
    version: $version,
    infrastructureType: $infrastructureType,
    infrastructureDetails: $infrastructureDetails,
    setupInterceptorsFilePath: $setupInterceptorsFilePath,
    setupInterceptorsLineNumber: $setupInterceptorsLineNumber,
    gitSha: $gitSha,
    gitOrg: $gitOrg,
    gitRepo: $gitRepo,
    gitProvider: $gitProvider,
    serviceDisplayName: $serviceDisplayName
  )
}`

const mutationCollectLogs = `mutation CollectLogs(
  $apiKey: String!,
  $serviceUuid: String!,
  $sessionId: String!,
  $level: String!,
  $contents: String!,
  $reentrancyGuardPreactive: Boolean!,
  $library: String!,
  $timestampMs: String!,
  $version: String!,
  $parentSpanId: String,
  $sourceFile: String,
  $sourceLine: Int
) {
  collectLogs(
    apiKey: $apiKey,
    serviceUuid: $serviceUuid,
    sessionId: $sessionId,
    level: $level,
    contents: $contents,
    reentrancyGuardPreactive: $reentrancyGuardPreactive,
    library: $library,
    timestampMs: $timestampMs,
    version: $version,
    parentSpanId: $parentSpanId,
    sourceFile: $sourceFile,
    sourceLine: $sourceLine
  )
}`

const mutationCollectPrintStatements = `mutation CollectPrintStatements(
  $apiKey: String!,
  $serviceUuid: String!,
  $sessionId: String!,
  $contents: String!,
  $reentrancyGuardPreactive: Boolean!,
  $library: String!,
  $timestampMs: String!,
  $version: String!,
  $parentSpanId: String,
  $sourceFile: String,
  $sourceLine: Int
) {
  collectPrintStatements(
    apiKey: $apiKey,
    serviceUuid: $serviceUuid,
    sessionId: $sessionId,
    contents: $contents,
    reentrancyGuardPreactive: $reentrancyGuardPreactive,
    library: $library,
    timestampMs: $timestampMs,
    version: $version,
    parentSpanId: $parentSpanId,
    sourceFile: $sourceFile,
    sourceLine: $sourceLine
  )
}`

const mutationCollectExceptions = `mutation CollectExceptions(
  $apiKey: String!,
  $serviceUuid: String!,
  $sessionId: String!,
  $exceptionMessage: String!,
  $wasCaught: Boolean!,
  $traceJson: String!,
  $reentrancyGuardPreactive: Boolean!,
  $library: String!,
  $timestampMs: String!,
  $version: String!,
  $isFromLocalService: Boolean!,
  $parentSpanId: String
) {
  collectExceptions(
    apiKey: $apiKey,
    serviceUuid: $serviceUuid,
    sessionId: $sessionId,
    exceptionMessage: $exceptionMessage,
    wasCaught: $wasCaught,
    traceJson: $traceJson,
    reentrancyGuardPreactive: $reentrancyGuardPreactive,
    library: $library,
    timestampMs: $timestampMs,
    version: $version,
    isFromLocalService: $isFromLocalService,
    parentSpanId: $parentSpanId
  )
}`

const mutationCollectFunctionSpans = `mutation CollectFunctionSpans(
  $apiKey: String!,
  $serviceUuid: String!,
  $sessionId: String!,
  $timestampMs: String!,
  $parentSpanId: String,
  $library: String!,
  $version: String!,
  $spanId: String!,
  $filePath: String!,
  $lineNumber: Int!,
  $columnNumber: Int!,
  $functionName: String!,
  $arguments: String!,
  $returnValue: String,
  $startTimeNs: String!,
  $durationNs: String!
) {
  collectFunctionSpans(
    apiKey: $apiKey,
    serviceUuid: $serviceUuid,
    sessionId: $sessionId,
    timestampMs: $timestampMs,
    parentSpanId: $parentSpanId,
    library: $library,
    version: $version,
    spanId: $spanId,
    filePath: $filePath,
    lineNumber: $lineNumber,
    columnNumber: $columnNumber,
    functionName: $functionName,
    arguments: $arguments,
    returnValue: $returnValue,
    startTimeNs: $startTimeNs,
    durationNs: $durationNs
  )
}`

const mutationCollectNetworkRequest = `mutation collectNetworkRequest($data: NetworkRequestInput!) {
  collectNetworkRequest(data: $data)
}`

const mutationCollectNetworkHops = `mutation collectNetworkHops(
  $apiKey: String!,
  $sessionId: String!,
  $timestampMs: String!,
  $line: String!,
  $column: String!,
  $name: String!,
  $entrypoint: String!,
  $serviceUuid: String
) {
  collectNetworkHops(
    apiKey: $apiKey,
    sessionId: $sessionId,
    timestampMs: $timestampMs,
    line: $line,
    column: $column,
    name: $name,
    entrypoint: $entrypoint,
    serviceUuid: $serviceUuid
  )
}`

const mutationCollectMetadata = `mutation CollectMetadata(
  $apiKey: String!,
  $serviceUuid: String!,
  $sessionId: String!,
  $userId: String!,
  $traitsJson: String!,
  $excludedFields: [String!]!,
  $library: String!,
  $timestampMs: String!,
  $version: String!,
  $override: Boolean!
) {
  collectMetadata(
    apiKey: $apiKey,
    serviceUuid: $serviceUuid,
    sessionId: $sessionId,
    userId: $userId,
    traitsJson: $traitsJson,
    excludedFields: $excludedFields,
    library: $library,
    timestampMs: $timestampMs,
    version: $version,
    override: $override
  )
}`

const mutationUpdateServiceDetails = `mutation UpdateServiceDetails(
  $apiKey: String!,
  $serviceUuid: String!,
  $timestampMs: String!,
  $serviceIdentifier: String,
  $serviceVersion: String,
  $serviceAdditionalMetadata: JSON,
  $gitSha: String,
  $gitOrg: String,
  $gitRepo: String,
  $gitProvider: String,
  $serviceDisplayName: String
) {
  updateServiceDetails(
    apiKey: $apiKey,
    serviceUuid: $serviceUuid,
    timestampMs: $timestampMs,
    serviceIdentifier: $serviceIdentifier,
    serviceVersion: $serviceVersion,
    serviceAdditionalMetadata: $serviceAdditionalMetadata,
    gitSha: $gitSha,
    gitOrg: $gitOrg,
    gitRepo: $gitRepo,
    gitProvider: $gitProvider,
    serviceDisplayName: $serviceDisplayName
  )
}`

const mutationDomainsToNotPassHeaderTo = `mutation DomainsToNotPassHeaderTo(
  $apiKey: String!,
  $serviceUuid: String!,
  $timestampMs: String!,
  $domains: [String!]!,
  $gitSha: String
) {
  domainsToNotPassHeaderTo(
    apiKey: $apiKey,
    serviceUuid: $serviceUuid,
    timestampMs: $timestampMs,
    domains: $domains,
    gitSha: $gitSha
  )
}`
