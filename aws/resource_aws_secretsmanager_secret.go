package aws

import (
	"fmt"
	"log"
	"time"

	"github.com/hashicorp/terraform/helper/resource"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/service/secretsmanager"
	"github.com/hashicorp/terraform/helper/schema"
	"github.com/hashicorp/terraform/helper/validation"
)

func resourceAwsSecretsManagerSecret() *schema.Resource {
	return &schema.Resource{
		Create: resourceAwsSecretsManagerSecretCreate,
		Read:   resourceAwsSecretsManagerSecretRead,
		Update: resourceAwsSecretsManagerSecretUpdate,
		Delete: resourceAwsSecretsManagerSecretDelete,
		Importer: &schema.ResourceImporter{
			State: schema.ImportStatePassthrough,
		},

		Schema: map[string]*schema.Schema{
			"arn": {
				Type:     schema.TypeString,
				Computed: true,
			},
			"description": {
				Type:     schema.TypeString,
				Optional: true,
			},
			"kms_key_id": {
				Type:     schema.TypeString,
				Optional: true,
			},
			"name": {
				Type:     schema.TypeString,
				Required: true,
				ForceNew: true,
			},
			"recovery_window_in_days": {
				Type:         schema.TypeInt,
				Optional:     true,
				Default:      30,
				ValidateFunc: validation.IntBetween(7, 30),
			},
			"rotation_enabled": {
				Type:     schema.TypeBool,
				Computed: true,
			},
			"rotation_lambda_arn": {
				Type:     schema.TypeString,
				Optional: true,
			},
			"rotation_rules": {
				Type:     schema.TypeList,
				Optional: true,
				MaxItems: 1,
				Elem: &schema.Resource{
					Schema: map[string]*schema.Schema{
						"automatically_after_days": {
							Type:     schema.TypeInt,
							Required: true,
						},
					},
				},
			},
			"tags": {
				Type:     schema.TypeMap,
				Optional: true,
			},
		},
	}
}

func resourceAwsSecretsManagerSecretCreate(d *schema.ResourceData, meta interface{}) error {
	conn := meta.(*AWSClient).secretsmanagerconn

	input := &secretsmanager.CreateSecretInput{
		Description: aws.String(d.Get("description").(string)),
		Name:        aws.String(d.Get("name").(string)),
	}

	if v, ok := d.GetOk("kms_key_id"); ok && v.(string) != "" {
		input.KmsKeyId = aws.String(v.(string))
	}

	log.Printf("[DEBUG] Creating Secrets Manager Secret: %s", input)
	output, err := conn.CreateSecret(input)
	if err != nil {
		return fmt.Errorf("error creating Secrets Manager Secret: %s", err)
	}

	d.SetId(aws.StringValue(output.ARN))

	if v, ok := d.GetOk("rotation_lambda_arn"); ok && v.(string) != "" {
		input := &secretsmanager.RotateSecretInput{
			RotationLambdaARN: aws.String(v.(string)),
			RotationRules:     expandSecretsManagerRotationRules(d.Get("rotation_rules").([]interface{})),
			SecretId:          aws.String(d.Id()),
		}

		log.Printf("[DEBUG] Enabling Secrets Manager Secret rotation: %s", input)
		err := resource.Retry(1*time.Minute, func() *resource.RetryError {
			_, err := conn.RotateSecret(input)
			if err != nil {
				// AccessDeniedException: Secrets Manager cannot invoke the specified Lambda function.
				if isAWSErr(err, "AccessDeniedException", "") {
					return resource.RetryableError(err)
				}
				return resource.NonRetryableError(err)
			}
			return nil
		})
		if err != nil {
			return fmt.Errorf("error enabling Secrets Manager Secret %q rotation: %s", d.Id(), err)
		}
	}

	if v, ok := d.GetOk("tags"); ok {
		input := &secretsmanager.TagResourceInput{
			SecretId: aws.String(d.Id()),
			Tags:     tagsFromMapSecretsManager(v.(map[string]interface{})),
		}

		log.Printf("[DEBUG] Tagging Secrets Manager Secret: %s", input)
		_, err := conn.TagResource(input)
		if err != nil {
			return fmt.Errorf("error tagging Secrets Manager Secret %q: %s", d.Id(), input)
		}
	}

	return resourceAwsSecretsManagerSecretRead(d, meta)
}

func resourceAwsSecretsManagerSecretRead(d *schema.ResourceData, meta interface{}) error {
	conn := meta.(*AWSClient).secretsmanagerconn

	input := &secretsmanager.DescribeSecretInput{
		SecretId: aws.String(d.Id()),
	}

	log.Printf("[DEBUG] Reading Secrets Manager Secret: %s", input)
	output, err := conn.DescribeSecret(input)
	if err != nil {
		if isAWSErr(err, secretsmanager.ErrCodeResourceNotFoundException, "") {
			log.Printf("[WARN] Secrets Manager Secret %q not found - removing from state", d.Id())
			d.SetId("")
			return nil
		}
		return fmt.Errorf("error reading Secrets Manager Secret: %s", err)
	}

	d.Set("arn", output.ARN)
	d.Set("description", output.Description)
	d.Set("kms_key_id", output.KmsKeyId)
	d.Set("name", output.Name)
	d.Set("rotation_enabled", output.RotationEnabled)

	if aws.BoolValue(output.RotationEnabled) {
		d.Set("rotation_lambda_arn", output.RotationLambdaARN)
		if err := d.Set("rotation_rules", flattenSecretsManagerRotationRules(output.RotationRules)); err != nil {
			return fmt.Errorf("error setting rotation_rules: %s", err)
		}
	} else {
		d.Set("rotation_lambda_arn", "")
		d.Set("rotation_rules", []interface{}{})
	}

	if err := d.Set("tags", tagsToMapSecretsManager(output.Tags)); err != nil {
		return fmt.Errorf("error setting tags: %s", err)
	}

	return nil
}

func resourceAwsSecretsManagerSecretUpdate(d *schema.ResourceData, meta interface{}) error {
	conn := meta.(*AWSClient).secretsmanagerconn

	if d.HasChange("description") || d.HasChange("kms_key_id") {
		input := &secretsmanager.UpdateSecretInput{
			Description: aws.String(d.Get("description").(string)),
			SecretId:    aws.String(d.Id()),
		}

		if v, ok := d.GetOk("kms_key_id"); ok && v.(string) != "" {
			input.KmsKeyId = aws.String(v.(string))
		}

		log.Printf("[DEBUG] Updating Secrets Manager Secret: %s", input)
		_, err := conn.UpdateSecret(input)
		if err != nil {
			return fmt.Errorf("error updating Secrets Manager Secret: %s", err)
		}
	}

	if d.HasChange("rotation_lambda_arn") || d.HasChange("rotation_rules") {
		if v, ok := d.GetOk("rotation_lambda_arn"); ok && v.(string) != "" {
			input := &secretsmanager.RotateSecretInput{
				RotationLambdaARN: aws.String(v.(string)),
				RotationRules:     expandSecretsManagerRotationRules(d.Get("rotation_rules").([]interface{})),
				SecretId:          aws.String(d.Id()),
			}

			log.Printf("[DEBUG] Enabling Secrets Manager Secret rotation: %s", input)
			err := resource.Retry(1*time.Minute, func() *resource.RetryError {
				_, err := conn.RotateSecret(input)
				if err != nil {
					// AccessDeniedException: Secrets Manager cannot invoke the specified Lambda function.
					if isAWSErr(err, "AccessDeniedException", "") {
						return resource.RetryableError(err)
					}
					return resource.NonRetryableError(err)
				}
				return nil
			})
			if err != nil {
				return fmt.Errorf("error updating Secrets Manager Secret %q rotation: %s", d.Id(), err)
			}
		} else {
			input := &secretsmanager.CancelRotateSecretInput{
				SecretId: aws.String(d.Id()),
			}

			log.Printf("[DEBUG] Cancelling Secrets Manager Secret rotation: %s", input)
			_, err := conn.CancelRotateSecret(input)
			if err != nil {
				return fmt.Errorf("error cancelling Secret Manager Secret %q rotation: %s", d.Id(), err)
			}
		}
	}

	if d.HasChange("tags") {
		oraw, nraw := d.GetChange("tags")
		o := oraw.(map[string]interface{})
		n := nraw.(map[string]interface{})
		create, remove := diffTagsSecretsManager(tagsFromMapSecretsManager(o), tagsFromMapSecretsManager(n))

		if len(remove) > 0 {
			log.Printf("[DEBUG] Removing Secrets Manager Secret %q tags: %#v", d.Id(), remove)
			k := make([]*string, len(remove), len(remove))
			for i, t := range remove {
				k[i] = t.Key
			}

			_, err := conn.UntagResource(&secretsmanager.UntagResourceInput{
				SecretId: aws.String(d.Id()),
				TagKeys:  k,
			})
			if err != nil {
				return fmt.Errorf("error updating Secrets Manager Secrets %q tags: %s", d.Id(), err)
			}
		}
		if len(create) > 0 {
			log.Printf("[DEBUG] Creating Secrets Manager Secret %q tags: %#v", d.Id(), create)
			_, err := conn.TagResource(&secretsmanager.TagResourceInput{
				SecretId: aws.String(d.Id()),
				Tags:     create,
			})
			if err != nil {
				return fmt.Errorf("error updating Secrets Manager Secrets %q tags: %s", d.Id(), err)
			}
		}
	}

	return resourceAwsSecretsManagerSecretRead(d, meta)
}

func resourceAwsSecretsManagerSecretDelete(d *schema.ResourceData, meta interface{}) error {
	conn := meta.(*AWSClient).secretsmanagerconn

	input := &secretsmanager.DeleteSecretInput{
		RecoveryWindowInDays: aws.Int64(int64(d.Get("recovery_window_in_days").(int))),
		SecretId:             aws.String(d.Id()),
	}

	log.Printf("[DEBUG] Deleting Secrets Manager Secret: %s", input)
	_, err := conn.DeleteSecret(input)
	if err != nil {
		if isAWSErr(err, secretsmanager.ErrCodeResourceNotFoundException, "") {
			return nil
		}
		return fmt.Errorf("error deleting Secrets Manager Secret: %s", err)
	}

	return nil
}

func expandSecretsManagerRotationRules(l []interface{}) *secretsmanager.RotationRulesType {
	if len(l) == 0 {
		return nil
	}

	m := l[0].(map[string]interface{})

	rules := &secretsmanager.RotationRulesType{
		AutomaticallyAfterDays: aws.Int64(int64(m["automatically_after_days"].(int))),
	}

	return rules
}

func flattenSecretsManagerRotationRules(rules *secretsmanager.RotationRulesType) []interface{} {
	if rules == nil {
		return []interface{}{}
	}

	m := map[string]interface{}{
		"automatically_after_days": int(aws.Int64Value(rules.AutomaticallyAfterDays)),
	}

	return []interface{}{m}
}