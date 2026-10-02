package policy

import "regexp"

// Partition is rgw::Partition (src/rgw/rgw_arn.h:13-19 at v19.2.6 and
// v20.2.4).
type Partition uint8

// The partitions, in rgw::Partition order.
const (
	PartitionAWS Partition = iota
	PartitionAWSCN
	PartitionAWSUSGov
	PartitionWildcard
)

// partitionNames are the names to_partition and ARN::to_string know
// (src/rgw/rgw_arn.cc:13-28, :192-200).
var partitionNames = [PartitionWildcard]string{
	PartitionAWS:      "aws",
	PartitionAWSCN:    "aws-cn",
	PartitionAWSUSGov: "aws-us-gov",
}

// Service is rgw::Service (src/rgw/rgw_arn.h:21-36 at v19.2.6 and v20.2.4),
// numbered in that enum's order, which is not the order of the name tables in
// rgw_arn.cc.
type Service uint8

// The services, in rgw::Service order.
const (
	ServiceApigateway Service = iota
	ServiceAppstream
	ServiceArtifact
	ServiceAutoscaling
	ServiceAWSPortal
	ServiceACM
	ServiceCloudformation
	ServiceCloudfront
	ServiceCloudhsm
	ServiceCloudsearch
	ServiceCloudtrail
	ServiceCloudwatch
	ServiceEvents
	ServiceLogs
	ServiceCodebuild
	ServiceCodecommit
	ServiceCodedeploy
	ServiceCodepipeline
	ServiceCognitoIDP
	ServiceCognitoIdentity
	ServiceCognitoSync
	ServiceConfig
	ServiceDatapipeline
	ServiceDMS
	ServiceDevicefarm
	ServiceDirectconnect
	ServiceDS
	ServiceDynamodb
	ServiceEC2
	ServiceECR
	ServiceECS
	ServiceSSM
	ServiceElasticbeanstalk
	ServiceElasticfilesystem
	ServiceElasticloadbalancing
	ServiceElasticmapreduce
	ServiceElastictranscoder
	ServiceElasticache
	ServiceES
	ServiceGamelift
	ServiceGlacier
	ServiceHealth
	ServiceIAM
	ServiceImportexport
	ServiceInspector
	ServiceIoT
	ServiceKMS
	ServiceKinesisanalytics
	ServiceFirehose
	ServiceKinesis
	ServiceLambda
	ServiceLightsail
	ServiceMachinelearning
	ServiceAWSMarketplace
	ServiceAWSMarketplaceManagement
	ServiceMobileanalytics
	ServiceMobilehub
	ServiceOpsworks
	ServiceOpsworksCM
	ServicePolly
	ServiceRedshift
	ServiceRDS
	ServiceRoute53
	ServiceRoute53domains
	ServiceSTS
	ServiceServicecatalog
	ServiceSES
	ServiceSNS
	ServiceSQS
	ServiceS3
	ServiceSWF
	ServiceSDB
	ServiceStates
	ServiceStoragegateway
	ServiceSupport
	ServiceTrustedadvisor
	ServiceWAF
	ServiceWorkmail
	ServiceWorkspaces
	ServiceWildcard
)

// serviceNames is the name table to_service and ARN::to_string share
// (src/rgw/rgw_arn.cc:32-112, :202-281), the same at v19.2.6 and v20.2.4.
var serviceNames = [ServiceWildcard]string{
	ServiceApigateway:               "apigateway",
	ServiceAppstream:                "appstream",
	ServiceArtifact:                 "artifact",
	ServiceAutoscaling:              "autoscaling",
	ServiceAWSPortal:                "aws-portal",
	ServiceACM:                      "acm",
	ServiceCloudformation:           "cloudformation",
	ServiceCloudfront:               "cloudfront",
	ServiceCloudhsm:                 "cloudhsm",
	ServiceCloudsearch:              "cloudsearch",
	ServiceCloudtrail:               "cloudtrail",
	ServiceCloudwatch:               "cloudwatch",
	ServiceEvents:                   "events",
	ServiceLogs:                     "logs",
	ServiceCodebuild:                "codebuild",
	ServiceCodecommit:               "codecommit",
	ServiceCodedeploy:               "codedeploy",
	ServiceCodepipeline:             "codepipeline",
	ServiceCognitoIDP:               "cognito-idp",
	ServiceCognitoIdentity:          "cognito-identity",
	ServiceCognitoSync:              "cognito-sync",
	ServiceConfig:                   "config",
	ServiceDatapipeline:             "datapipeline",
	ServiceDMS:                      "dms",
	ServiceDevicefarm:               "devicefarm",
	ServiceDirectconnect:            "directconnect",
	ServiceDS:                       "ds",
	ServiceDynamodb:                 "dynamodb",
	ServiceEC2:                      "ec2",
	ServiceECR:                      "ecr",
	ServiceECS:                      "ecs",
	ServiceSSM:                      "ssm",
	ServiceElasticbeanstalk:         "elasticbeanstalk",
	ServiceElasticfilesystem:        "elasticfilesystem",
	ServiceElasticloadbalancing:     "elasticloadbalancing",
	ServiceElasticmapreduce:         "elasticmapreduce",
	ServiceElastictranscoder:        "elastictranscoder",
	ServiceElasticache:              "elasticache",
	ServiceES:                       "es",
	ServiceGamelift:                 "gamelift",
	ServiceGlacier:                  "glacier",
	ServiceHealth:                   "health",
	ServiceIAM:                      "iam",
	ServiceImportexport:             "importexport",
	ServiceInspector:                "inspector",
	ServiceIoT:                      "iot",
	ServiceKMS:                      "kms",
	ServiceKinesisanalytics:         "kinesisanalytics",
	ServiceFirehose:                 "firehose",
	ServiceKinesis:                  "kinesis",
	ServiceLambda:                   "lambda",
	ServiceLightsail:                "lightsail",
	ServiceMachinelearning:          "machinelearning",
	ServiceAWSMarketplace:           "aws-marketplace",
	ServiceAWSMarketplaceManagement: "aws-marketplace-management",
	ServiceMobileanalytics:          "mobileanalytics",
	ServiceMobilehub:                "mobilehub",
	ServiceOpsworks:                 "opsworks",
	ServiceOpsworksCM:               "opsworks-cm",
	ServicePolly:                    "polly",
	ServiceRedshift:                 "redshift",
	ServiceRDS:                      "rds",
	ServiceRoute53:                  "route53",
	ServiceRoute53domains:           "route53domains",
	ServiceSTS:                      "sts",
	ServiceServicecatalog:           "servicecatalog",
	ServiceSES:                      "ses",
	ServiceSNS:                      "sns",
	ServiceSQS:                      "sqs",
	ServiceS3:                       "s3",
	ServiceSWF:                      "swf",
	ServiceSDB:                      "sdb",
	ServiceStates:                   "states",
	ServiceStoragegateway:           "storagegateway",
	ServiceSupport:                  "support",
	ServiceTrustedadvisor:           "trustedadvisor",
	ServiceWAF:                      "waf",
	ServiceWorkmail:                 "workmail",
	ServiceWorkspaces:               "workspaces",
}

var servicesByName = func() map[string]Service {
	m := make(map[string]Service, len(serviceNames))
	for s := range ServiceWildcard {
		m[serviceNames[s]] = s
	}
	return m
}()

// ARN is rgw::ARN (src/rgw/rgw_arn.h:42-68),
// arn:<partition>:<service>:<region>:<account>:<resource>. The account is the
// tenant or account id a resource belongs to. rgw_arn.h and rgw_arn.cc are
// the same at v19.2.6 and v20.2.4, so each citation here holds at both.
type ARN struct {
	Partition Partition
	Service   Service
	Region    string
	Account   string
	Resource  string
}

// BucketARN is ARN(const rgw_bucket&) (src/rgw/rgw_arn.cc:137-142): aws, s3,
// no region, the bucket's tenant and its name.
func BucketARN(tenant, bucket string) ARN {
	return ARN{Partition: PartitionAWS, Service: ServiceS3, Account: tenant, Resource: bucket}
}

// ObjectARN is ARN(const rgw_obj&) (src/rgw/rgw_arn.cc:126-135): BucketARN
// with "/<key>" appended to the resource. key is the object's name; the
// version instance is not part of the ARN.
func ObjectARN(tenant, bucket, key string) ARN {
	return ARN{Partition: PartitionAWS, Service: ServiceS3, Account: tenant, Resource: bucket + "/" + key}
}

// IAMARN is ARN(resource_name, type, tenant, has_path)
// (src/rgw/rgw_arn.cc:154-163): aws, iam, no region, the tenant, and
// "<typ>/<name>", or "<typ><name>" when hasPath says name already starts with
// its path.
func IAMARN(name, typ, tenant string, hasPath bool) ARN {
	resource := typ
	if !hasPath {
		resource += "/"
	}
	return ARN{Partition: PartitionAWS, Service: ServiceIAM, Account: tenant, Resource: resource + name}
}

// The regular expressions of ARN::parse (src/rgw/rgw_arn.cc:166-172 at
// v19.2.6 and v20.2.4), which std::regex_match anchors at both ends.
// libstdc++'s ECMAScript '.' matches neither '\n' nor '\r', so the resource
// of an ARN parsed without wildcards may hold neither; Go's '.' would admit
// '\r'.
var (
	arnWildcards   = regexp.MustCompile(`\Aarn:([^:]*):([^:]*):([^:]*):([^:]*):([^:]*)\z`)
	arnNoWildcards = regexp.MustCompile(`\Aarn:([^:*]*):([^:*]*):([^:*]*):([^:*]*):([^\n\r]*)\z`)
)

// ParseARN is ARN::parse (src/rgw/rgw_arn.cc:165-187). With wildcards, "*"
// alone is the ARN whose every field is a wildcard, the partition and the
// service may each be exactly "*", and no field, the resource included, may
// hold ':'. Without wildcards the four fields before the resource may hold
// neither ':' nor '*', and the resource may hold anything but a line break.
func ParseARN(s string, wildcards bool) (ARN, bool) {
	if wildcards && s == "*" {
		return ARN{Partition: PartitionWildcard, Service: ServiceWildcard, Region: "*", Account: "*", Resource: "*"}, true
	}
	rx := arnNoWildcards
	if wildcards {
		rx = arnWildcards
	}
	m := rx.FindStringSubmatch(s)
	if m == nil {
		return ARN{}, false
	}
	p, ok := parsePartition(m[1], wildcards)
	if !ok {
		return ARN{}, false
	}
	svc, ok := parseService(m[2], wildcards)
	if !ok {
		return ARN{}, false
	}
	return ARN{Partition: p, Service: svc, Region: m[3], Account: m[4], Resource: m[5]}, true
}

// parsePartition is to_partition (src/rgw/rgw_arn.cc:13-28).
func parsePartition(s string, wildcards bool) (Partition, bool) {
	if wildcards && s == "*" {
		return PartitionWildcard, true
	}
	for p := range PartitionWildcard {
		if s == partitionNames[p] {
			return p, true
		}
	}
	return 0, false
}

// parseService is to_service (src/rgw/rgw_arn.cc:30-124), whose names are
// matched exactly, case included.
func parseService(s string, wildcards bool) (Service, bool) {
	if wildcards && s == "*" {
		return ServiceWildcard, true
	}
	svc, ok := servicesByName[s]
	return svc, ok
}

// String is ARN::to_string (src/rgw/rgw_arn.cc:189-300): a partition or
// service with no name, the wildcards included, renders as "*".
func (a ARN) String() string {
	partition, service := "*", "*"
	if a.Partition < PartitionWildcard {
		partition = partitionNames[a.Partition]
	}
	if a.Service < ServiceWildcard {
		service = serviceNames[a.Service]
	}
	return "arn:" + partition + ":" + service + ":" + a.Region + ":" + a.Account + ":" + a.Resource
}

// Match is ARN::match (src/rgw/rgw_arn.cc:319-344) with a as the pattern. A
// candidate whose partition or service is a wildcard never matches; the
// region and the account match with MatchWildcards folding case, the
// resource without.
func (a ARN) Match(candidate ARN) bool {
	if candidate.Partition == PartitionWildcard ||
		a.Partition != candidate.Partition && a.Partition != PartitionWildcard {
		return false
	}
	if candidate.Service == ServiceWildcard ||
		a.Service != candidate.Service && a.Service != ServiceWildcard {
		return false
	}
	return MatchWildcards(a.Region, candidate.Region, true) &&
		MatchWildcards(a.Account, candidate.Account, true) &&
		MatchWildcards(a.Resource, candidate.Resource, false)
}
