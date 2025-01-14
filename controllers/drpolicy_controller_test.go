package controllers_test

import (
	"context"
	"encoding/json"
	"fmt"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	. "github.com/onsi/gomega/gstruct"
	gomegaTypes "github.com/onsi/gomega/types"
	workv1 "github.com/open-cluster-management/api/work/v1"
	ramen "github.com/ramendr/ramen/api/v1alpha1"
	"github.com/ramendr/ramen/controllers"
	"github.com/ramendr/ramen/controllers/util"
	plrv1 "github.com/stolostron/multicloud-operators-placementrule/pkg/apis/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/validation/field"
	validationErrors "k8s.io/kube-openapi/pkg/validation/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var _ = Describe("DrpolicyController", func() {
	clusterNamesCurrent := &sets.String{}
	clusterNames := func(drpolicy *ramen.DRPolicy) sets.String {
		return sets.NewString(util.DrpolicyClusterNames(drpolicy)...)
	}
	drClustersExpect := func() {
		Eventually(
			func(g Gomega) {
				clusterNames := sets.String{}
				g.Expect(controllers.DrClustersDeployedSet(context.TODO(), k8sClient, &clusterNames)).To(Succeed())
				g.Expect(clusterNames.UnsortedList()).To(ConsistOf(clusterNamesCurrent.UnsortedList()))
			},
			timeout,
			interval,
		).Should(Succeed())
	}
	validatedConditionExpect := func(drpolicy *ramen.DRPolicy, status metav1.ConditionStatus,
		messageMatcher gomegaTypes.GomegaMatcher,
	) {
		Eventually(
			func(g Gomega) {
				g.Expect(apiReader.Get(
					context.TODO(),
					types.NamespacedName{Name: drpolicy.Name},
					drpolicy,
				)).To(Succeed())
				g.Expect(drpolicy.Status.Conditions).To(MatchElements(
					func(element interface{}) string {
						return element.(metav1.Condition).Type
					},
					IgnoreExtras,
					Elements{
						ramen.DRPolicyValidated: MatchAllFields(Fields{
							`Type`:               Ignore(),
							`Status`:             Equal(status),
							`ObservedGeneration`: Equal(drpolicy.Generation),
							`LastTransitionTime`: Ignore(),
							`Reason`:             Ignore(),
							`Message`:            messageMatcher,
						}),
					},
				))
			},
			timeout,
			interval,
		).Should(Succeed())
	}
	drpolicyCreate := func(drpolicy *ramen.DRPolicy) {
		for _, clusterName := range clusterNames(drpolicy).Difference(*clusterNamesCurrent).UnsortedList() {
			Expect(k8sClient.Create(
				context.TODO(),
				&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: clusterName}},
			)).To(Succeed())
			*clusterNamesCurrent = clusterNamesCurrent.Insert(clusterName)
		}
		Expect(k8sClient.Create(context.TODO(), drpolicy)).To(Succeed())
		drClustersExpect()
	}
	drpolicyUpdate := func(drpolicy *ramen.DRPolicy) {
		Expect(k8sClient.Update(context.TODO(), drpolicy)).To(Succeed())
	}
	drpolicyDeleteAndConfirm := func(drpolicy *ramen.DRPolicy) {
		Expect(k8sClient.Delete(context.TODO(), drpolicy)).To(Succeed())
		Eventually(func() bool {
			return errors.IsNotFound(apiReader.Get(context.TODO(), types.NamespacedName{Name: drpolicy.Name}, drpolicy))
		}, timeout, interval).Should(BeTrue())
	}
	namespaceDeleteAndConfirm := func(namespaceName string) {
		// TODO: debug namespace delete not finalized
		if true {
			return
		}
		namespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespaceName}}
		Expect(k8sClient.Delete(context.TODO(), namespace)).To(Succeed())
		Eventually(func() bool {
			err := apiReader.Get(context.TODO(), types.NamespacedName{Name: namespaceName}, namespace)
			s, _ := (json.MarshalIndent(*namespace, "", "  "))
			fmt.Println(string(s))

			return errors.IsNotFound(err)
		}, timeout*3, interval).Should(BeTrue())
	}
	drClusterManifestWorkGet := func(clusterName string, manifestWork *workv1.ManifestWork) error {
		return apiReader.Get(
			context.TODO(),
			types.NamespacedName{
				Name:      util.DrClusterManifestWorkName,
				Namespace: clusterName,
			},
			manifestWork,
		)
	}
	drpolicyDelete := func(drpolicy *ramen.DRPolicy, clusterNamesExpected sets.String) {
		drpolicyDeleteAndConfirm(drpolicy)
		drClusterManifestWorkAbsenceExpect := func(clusterName string) {
			Eventually(func() bool {
				manifestWork := &workv1.ManifestWork{}

				return errors.IsNotFound(drClusterManifestWorkGet(clusterName, manifestWork))
			}, timeout, interval).Should(BeTrue())
		}
		for _, clusterName := range clusterNamesCurrent.Difference(clusterNamesExpected).UnsortedList() {
			drClusterManifestWorkAbsenceExpect(clusterName)
			namespaceDeleteAndConfirm(clusterName)
			*clusterNamesCurrent = clusterNamesCurrent.Delete(clusterName)
		}
		drClustersExpect()
	}
	cidrs := [][]string{
		{"198.51.100.17/24", "198.51.100.18/24", "198.51.100.19/24"}, // valid CIDR
		{"1111.51.100.14/24", "aaa.51.100.15/24", "00.51.100.16/24"}, // invalid CIDR
	}
	s3SecretStringData := func(accessID, secretKey string) map[string]string {
		return map[string]string{
			"AWS_ACCESS_KEY_ID":     accessID,
			"AWS_SECRET_ACCESS_KEY": secretKey,
		}
	}
	s3SecretCreate := func(s3Secret *corev1.Secret) {
		Expect(k8sClient.Create(context.TODO(), s3Secret)).To(Succeed())
	}
	s3SecretUpdate := func(s3Secret *corev1.Secret) {
		Expect(k8sClient.Update(context.TODO(), s3Secret)).To(Succeed())
	}
	s3SecretUpdateAccessID := func(s3Secret *corev1.Secret, accessID string) {
		secretFetched := &corev1.Secret{}
		Expect(k8sClient.Get(
			context.TODO(),
			types.NamespacedName{Name: s3Secret.Name, Namespace: s3Secret.Namespace},
			secretFetched)).To(Succeed())
		secretFetched.StringData = s3SecretStringData(accessID, secretFetched.StringData["AWS_SECRET_ACCESS_KEY"])
		s3SecretUpdate(secretFetched)
	}
	s3SecretDelete := func(s3Secret *corev1.Secret) {
		Expect(k8sClient.Delete(context.TODO(), s3Secret)).To(Succeed())
	}
	s3Secrets := [...]corev1.Secret{
		{
			ObjectMeta: metav1.ObjectMeta{Name: "s3secret0"},
			StringData: s3SecretStringData(awsAccessKeyIDSucc, ""),
		},
	}
	var s3SecretObjectMetas [len(s3Secrets)]metav1.ObjectMeta
	s3SecretObjectMetaReset := func(i uint) {
		s3Secrets[i].ObjectMeta = s3SecretObjectMetas[i]
	}
	s3SecretsNamespaceNameSet := func() {
		namespaceName := configMap.Namespace
		for i := range s3Secrets {
			s3Secrets[i].Namespace = namespaceName
			s3SecretObjectMetas[i] = s3Secrets[i].ObjectMeta
		}
	}
	s3SecretsCreate := func() {
		for i := range s3Secrets {
			s3SecretCreate(&s3Secrets[i])
		}
	}
	s3SecretsDelete := func() {
		for i := range s3Secrets {
			s3SecretDelete(&s3Secrets[i])
		}
	}
	// For each policy combination that may exist, add an entry for use in ensuring secret is created as desired:
	// - Initial map takes keys that are ordered combinations of drPolicy names that may co-exist
	// - Internal map takes kyes that are secret names with a list of strings as its value containing the cluster
	// list that it should be available on
	drPoliciesAndSecrets := map[string]map[string][]string{
		"drpolicy0": {
			"s3secret0": {"cluster0", "cluster1"},
		},
		"drpolicy1": {
			"s3secret0": {"cluster1", "cluster2"},
		},
		"drpolicy0drpolicy1": {
			"s3secret0": {"cluster0", "cluster1", "cluster2"},
		},
	}
	var plRuleNames [len(s3Secrets)]string
	s3SecretsPolicyNamesSet := func() {
		for idx := range s3Secrets {
			_, _, plRuleNames[idx], _ = util.GeneratePolicyResourceNames(s3Secrets[idx].Name)
		}
	}
	plRuleContains := func(plRule plrv1.PlacementRule, clusters []string) bool {
		for _, cluster := range clusters {
			found := false
			for _, specCluster := range plRule.Spec.Clusters {
				if specCluster.Name == cluster {
					found = true

					break
				}
			}

			if !found {
				return false
			}
		}

		return true
	}
	getPlRuleForSecrets := func() []plrv1.PlacementRule {
		plRuleList := &plrv1.PlacementRuleList{}
		listOptions := &client.ListOptions{Namespace: configMap.Namespace}

		Expect(apiReader.List(context.TODO(), plRuleList, listOptions)).NotTo(HaveOccurred())

		foundPlRules := []plrv1.PlacementRule{}
		for _, plRule := range plRuleList.Items {
			for _, plRuleName := range plRuleNames {
				if plRule.Name != plRuleName {
					continue
				}
				foundPlRules = append(foundPlRules, plRule)

				break
			}
		}

		return foundPlRules
	}
	vaildateSecretDistribution := func(drPolicies []ramen.DRPolicy) {
		plRules := getPlRuleForSecrets()

		// If no policies are present, expect no secret placement rules
		if drPolicies == nil {
			Expect(len(plRules)).To(Equal(0))

			return
		}

		// Construct drpolicies name
		policyCombinationName := ""
		for _, drpolicy := range drPolicies {
			policyCombinationName += drpolicy.Name
		}

		// Ensure list of secrets for the policy name has as many placement rules
		Expect(len(plRules) == len(drPoliciesAndSecrets[policyCombinationName])).To(BeTrue())

		// Range through secrets in drpolicies name and ensure cluster list is the same
		for secretName, clusterList := range drPoliciesAndSecrets[policyCombinationName] {
			found := false
			_, _, plRuleName, _ := util.GeneratePolicyResourceNames(secretName)

			for _, plRule := range plRules {
				if plRule.Name != plRuleName {
					continue
				}
				Expect(plRuleContains(plRule, clusterList)).To(BeTrue())
				found = true

				break
			}
			Expect(found).To(BeTrue())
		}
	}
	var s3SecretNumber uint = 0
	s3ProfileNew := func(profileNameSuffix, bucketName string) ramen.S3StoreProfile {
		return ramen.S3StoreProfile{
			S3ProfileName:        "s3profile" + profileNameSuffix,
			S3Bucket:             bucketName,
			S3CompatibleEndpoint: "http://192.168.39.223:30000",
			S3Region:             "us-east-1",
			S3SecretRef:          corev1.SecretReference{Name: s3Secrets[s3SecretNumber].Name},
		}
	}
	s3Profiles := []ramen.S3StoreProfile{
		s3ProfileNew("0", bucketNameSucc),
		s3ProfileNew("1", bucketNameSucc2),
		s3ProfileNew("2", bucketNameFail),
		s3ProfileNew("3", bucketNameFail2),
	}
	s3ProfilesSecretNamespaceNameSet := func() {
		namespaceName := s3Secrets[s3SecretNumber].Namespace
		for i := range s3Profiles {
			s3Profiles[i].S3SecretRef.Namespace = namespaceName
		}
	}
	s3ProfilesUpdate := func() {
		s3ProfilesStore(s3Profiles)
	}
	Specify("s3 profiles and secrets", func() {
		s3SecretsNamespaceNameSet()
		s3SecretsPolicyNamesSet()
		s3SecretsCreate()
		s3ProfilesSecretNamespaceNameSet()
		s3ProfilesUpdate()
	})
	clusters := [...]ramen.ManagedCluster{
		{Name: "cluster0", S3ProfileName: s3Profiles[0].S3ProfileName, Region: "east"},
		{Name: "cluster1", S3ProfileName: s3Profiles[0].S3ProfileName, Region: "west"},
		{Name: "cluster2", S3ProfileName: s3Profiles[0].S3ProfileName, Region: "east"},
	}
	drpolicies := [...]ramen.DRPolicy{
		{
			ObjectMeta: metav1.ObjectMeta{Name: "drpolicy0"},
			Spec:       ramen.DRPolicySpec{DRClusterSet: clusters[0:2], SchedulingInterval: `00m`},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "drpolicy1"},
			Spec:       ramen.DRPolicySpec{DRClusterSet: clusters[1:3], SchedulingInterval: `9999999d`},
		},
	}
	var drpolicyObjectMetas [len(drpolicies)]metav1.ObjectMeta
	func() {
		for i := range drpolicies {
			drpolicyObjectMetas[i] = drpolicies[i].ObjectMeta
		}
	}()
	drpolicyObjectMetaReset := func(i uint) {
		drpolicies[i].ObjectMeta = drpolicyObjectMetas[i]
	}
	clusterNamesNone := sets.String{}
	var drpolicy *ramen.DRPolicy
	var drpolicyNumber uint
	Specify(`a drpolicy`, func() {
		drpolicyNumber = 0
		drpolicy = &drpolicies[drpolicyNumber]
	})

	drClusterOperatorDeploymentAutomationEnableOrDisable := func(enable bool) {
		clusterNames := util.DrpolicyClusterNames(drpolicy)
		manifestWorks := make([]workv1.ManifestWork, len(clusterNames))
		for i, clusterName := range clusterNames {
			Expect(drClusterManifestWorkGet(clusterName, &manifestWorks[i])).To(Succeed())
		}
		ramenConfig.DrClusterOperator.DeploymentAutomationEnabled = enable
		ramenConfig.DrClusterOperator.S3SecretDistributionEnabled = enable
		configMapUpdate()
		for i := range manifestWorks {
			manifestWork := &manifestWorks[i]
			expectedCount := 2
			if enable {
				expectedCount = 8
			}
			clusterName := manifestWork.GetNamespace()
			Eventually(func() bool {
				if err := drClusterManifestWorkGet(clusterName, manifestWork); err != nil {
					return false
				}

				return len(manifestWork.Spec.Workload.Manifests) == expectedCount
			}, timeout, interval).Should(BeTrue())
		}
		validatedConditionExpect(drpolicy, metav1.ConditionTrue, Ignore())
	}

	When("a drpolicy is created specifying a cluster name and a namespace of the same name does not exist", func() {
		It("should set its validated status condition's status to false", func() {
			Expect(k8sClient.Create(context.TODO(), drpolicy)).To(Succeed())
			validatedConditionExpect(drpolicy, metav1.ConditionFalse, Ignore())
		})
	})
	Specify("drpolicy delete", func() {
		drpolicyDeleteAndConfirm(drpolicy)
	})
	Specify("a drpolicy", func() {
		drpolicyObjectMetaReset(drpolicyNumber)
	})
	When("a drpolicy with valid CIDRs", func() {
		It("should succeed", func() {
			drpolicy.Spec.DRClusterSet[0].CIDRs = cidrs[0]
			drpolicyCreate(drpolicy)
			validatedConditionExpect(drpolicy, metav1.ConditionTrue, Ignore())
		})
	})
	When("a valid drpolicy's ramen config is updated to enable drcluster operator installation automation", func() {
		It("should increase the manifest count for each of its managed clusters", func() {
			drClusterOperatorDeploymentAutomationEnableOrDisable(true)
		})
	})
	When("a drpolicy with invalid CIDRs", func() {
		It("should set validation status to false", func() {
			drpolicy.Spec.DRClusterSet[0].CIDRs = cidrs[1]
			drpolicyUpdate(drpolicy)
			validatedConditionExpect(drpolicy, metav1.ConditionFalse, Ignore())
		})
	})
	Specify("remove invalid CIDRs and update", func() {
		drpolicy.Spec.DRClusterSet[0].CIDRs = nil
		drpolicyUpdate(drpolicy)
		validatedConditionExpect(drpolicy, metav1.ConditionTrue, Ignore())
	})
	Specify("drpolicy delete", func() {
		drpolicyDeleteAndConfirm(drpolicy)
		vaildateSecretDistribution(nil)
	})
	Specify("a drpolicy", func() {
		drpolicyObjectMetaReset(drpolicyNumber)
	})
	When("a 1st drpolicy is created", func() {
		It("should create a drcluster manifest work for each cluster specified in a 1st drpolicy", func() {
			drpolicyCreate(drpolicy)
			validatedConditionExpect(drpolicy, metav1.ConditionTrue, Ignore())
			vaildateSecretDistribution(drpolicies[0:1])
		})
	})
	When("a drpolicy is created referencing an s3 profile that connects successfully", func() {
		It("should set its validated status condition's status to true", func() {
			validatedConditionExpect(drpolicy, metav1.ConditionTrue, Ignore())
		})
	})
	When("TODO a 1st drpolicy is updated to add some clusters and remove some other clusters", func() {
		It("should create a drcluster manifest work for each cluster added and "+
			"delete a drcluster manifest work for each cluster removed", func() {
		})
	})
	When("a 2nd drpolicy is created specifying some clusters in a 1st drpolicy and some not", func() {
		It("should create a drcluster manifest work for each cluster specified in a 2nd drpolicy but not a 1st drpolicy",
			func() {
				drpolicyCreate(&drpolicies[1])
				validatedConditionExpect(&drpolicies[1], metav1.ConditionTrue, Ignore())
				vaildateSecretDistribution(drpolicies[0:2])
			},
		)
	})
	When("a 1st drpolicy is deleted", func() {
		It("should delete a drcluster manifest work for each cluster specified in a 1st drpolicy but not a 2nd drpolicy",
			func() {
				drpolicyDelete(drpolicy, clusterNames(&drpolicies[1]))
				vaildateSecretDistribution(drpolicies[1:2])
			},
		)
	})
	When("a 2nd drpolicy is deleted", func() {
		It("should delete a drcluster manifest work for each cluster specified in a 2nd drpolicy", func() {
			drpolicyDelete(&drpolicies[1], clusterNamesNone)
			vaildateSecretDistribution(nil)
		})
	})
	Specify(`a drpolicy`, func() {
		drpolicyObjectMetaReset(drpolicyNumber)
	})
	When(`a drpolicy creation request contains an invalid scheduling interval`, func() {
		It(`should fail`, func() {
			err := func() *errors.StatusError {
				path := field.NewPath(`spec`, `schedulingInterval`)
				value := ``

				return errors.NewInvalid(
					schema.GroupKind{
						Group: ramen.GroupVersion.Group,
						Kind:  `DRPolicy`,
					},
					drpolicy.Name,
					field.ErrorList{
						field.Invalid(
							path,
							value,
							validationErrors.FailedPattern(
								path.String(),
								`body`,
								`^\d+[mhd]$`,
								value,
							).Error(),
						),
					},
				)
			}()
			drpolicy.Spec.SchedulingInterval = `3s`
			Expect(k8sClient.Create(context.TODO(), drpolicy)).To(MatchError(err))
			drpolicy.Spec.SchedulingInterval = `0`
			Expect(k8sClient.Create(context.TODO(), drpolicy)).To(MatchError(err))
		})
	})
	Specify(`a drpolicy`, func() {
		drpolicy.Spec.SchedulingInterval = `00m`
	})
	When("a drpolicy is created referencing an s3 profile that connects unsuccessfully", func() {
		It("should set its validated status condition's status to false and "+
			"message to specify the name of the first listed s3 profile that connected unsuccessfully", func() {
			s3ProfileName := s3Profiles[2].S3ProfileName
			drpolicy.Spec.DRClusterSet[1].S3ProfileName = s3ProfileName
			Expect(k8sClient.Create(context.TODO(), drpolicy)).To(Succeed())
			validatedConditionExpect(drpolicy, metav1.ConditionFalse, HavePrefix(s3ProfileName+": "))
		})
	})
	When("a drpolicy is updated referencing an s3 profile that connects unsuccessfully "+
		"ordered before one that previously connected unsuccessfully", func() {
		It("should change its validated status condition"+
			"message to specify the name of the first listed s3 profile that connected unsuccessfully", func() {
			s3ProfileName := s3Profiles[3].S3ProfileName
			drpolicy.Spec.DRClusterSet[0].S3ProfileName = s3ProfileName
			drpolicyUpdate(drpolicy)
			validatedConditionExpect(drpolicy, metav1.ConditionFalse, HavePrefix(s3ProfileName+": "))
		})
	})
	When("a drpolicy is updated referencing s3 profiles that all connect successfully", func() {
		It("should update its validated status condition's status to true", func() {
			s3ProfileName := s3Profiles[0].S3ProfileName
			drpolicy.Spec.DRClusterSet[0].S3ProfileName = s3ProfileName
			drpolicy.Spec.DRClusterSet[1].S3ProfileName = s3ProfileName
			drpolicyUpdate(drpolicy)
			validatedConditionExpect(drpolicy, metav1.ConditionTrue, Ignore())
			vaildateSecretDistribution(drpolicies[0:1])
		})
	})
	When("a valid drpolicy is updated referencing a different s3 profile that connects successfully", func() {
		It("should update its validated status condition's observed generation", func() {
			s3ProfileName := s3Profiles[1].S3ProfileName
			drpolicy.Spec.DRClusterSet[0].S3ProfileName = s3ProfileName
			drpolicyUpdate(drpolicy)
			validatedConditionExpect(drpolicy, metav1.ConditionTrue, Ignore())
			vaildateSecretDistribution(drpolicies[0:1])
		})
	})
	var s3Profile *ramen.S3StoreProfile
	Specify("an s3 profile", func() {
		s3Profile = &s3Profiles[2]
	})
	When("a valid drpolicy is updated referencing an s3 profile connects unsuccessfully", func() {
		It("should update its validated status condition's status to false", func() {
			drpolicy.Spec.DRClusterSet[0].S3ProfileName = s3Profile.S3ProfileName
			drpolicyUpdate(drpolicy)
			validatedConditionExpect(drpolicy, metav1.ConditionFalse, HavePrefix(s3Profile.S3ProfileName+": "))
		})
	})
	drpolicyFix := func() {
		When("an invalid drpolicy's referenced s3 profile is updated to connect successfully", func() {
			It("shoud update its validated status condition's status to true", func() {
				s3Profile.S3Bucket = bucketNameSucc
				s3ProfilesUpdate()
				validatedConditionExpect(drpolicy, metav1.ConditionTrue, Ignore())
				vaildateSecretDistribution(drpolicies[0:1])
			})
		})
	}
	drpolicyFix()
	When("a valid drpolicy's referenced s3 profile is updated to connect unsuccessfully", func() {
		It("shoud update its validated status condition's status to false", func() {
			s3Profile.S3Bucket = bucketNameFail
			s3ProfilesUpdate()
			validatedConditionExpect(drpolicy, metav1.ConditionFalse, HavePrefix(s3Profile.S3ProfileName+": "))
		})
	})
	drpolicyFix()
	var s3Secret *corev1.Secret
	Specify("s3 secret", func() {
		s3Secret = &s3Secrets[s3SecretNumber]
		Expect(s3Profile.S3SecretRef.Namespace).To(Equal(s3Secret.Namespace))
		Expect(s3Profile.S3SecretRef.Name).To(Equal(s3Secret.Name))
	})
	When("a valid drpolicy's referenced s3 profile's secret is updated to connect unsuccessfully", func() {
		It("shoud update its validated status condition's status to false", func() {
			s3SecretUpdateAccessID(s3Secret, awsAccessKeyIDFail)
			validatedConditionExpect(drpolicy, metav1.ConditionFalse, HavePrefix(s3Profile.S3ProfileName+": "))
		})
	})
	When("an invalid drpolicy's referenced s3 profile's secret is updated to connect successfully", func() {
		It("shoud update its validated status condition's status to true", func() {
			s3SecretUpdateAccessID(s3Secret, awsAccessKeyIDSucc)
			validatedConditionExpect(drpolicy, metav1.ConditionTrue, Ignore())
		})
	})
	When("a valid drpolicy's referenced s3 profile's secret is deleted", func() {
		It("should update its validated status condition's status to false", func() {
			s3SecretDelete(s3Secret)
			validatedConditionExpect(drpolicy, metav1.ConditionFalse, HavePrefix(s3Profile.S3ProfileName+": "))
			vaildateSecretDistribution(nil)
		})
	})
	When("an invalid drpolicy's referenced s3 profile's secret is re-created", func() {
		It("should update its validated status condition's status to true", func() {
			s3SecretObjectMetaReset(s3SecretNumber)
			s3SecretCreate(s3Secret)
			validatedConditionExpect(drpolicy, metav1.ConditionTrue, Ignore())
			vaildateSecretDistribution(drpolicies[0:1])
		})
	})
	When("a valid drpolicy's ramen config is updated to disable drcluster operator installation automation", func() {
		It("should decrease the manifest count for each of its managed clusters", func() {
			drClusterOperatorDeploymentAutomationEnableOrDisable(false)
		})
	})
	Specify(`drpolicy delete`, func() {
		drpolicyDeleteAndConfirm(drpolicy)
	})
	Specify("s3 secrets delete", func() {
		s3SecretsDelete()
	})
})
