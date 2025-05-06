from django.db import models
from django.utils import timezone

# Create your models here.


class InferenceRequest(models.Model):
    INFERENCE_TYPES = [
        ("llm_chat", "LLM Chat"),
        ("llm_completion", "LLM Completion"),
        ("image_generation", "Image Generation"),
    ]

    STATUS_CHOICES = [
        ("requested", "Requested"),
        ("in_progress", "In Progress"),
        ("completed", "Completed"),
        ("failed", "Failed"),
    ]

    inference_type = models.CharField(max_length=20, choices=INFERENCE_TYPES)
    payload = models.JSONField()
    status = models.CharField(
        max_length=20, choices=STATUS_CHOICES, default="requested"
    )
    response = models.JSONField(null=True, blank=True)
    error_details = models.TextField(null=True, blank=True)
    generated_image = models.ImageField(
        upload_to="generated_images/", null=True, blank=True
    )
    created_at = models.DateTimeField(default=timezone.now)
    updated_at = models.DateTimeField(auto_now=True)

    class Meta:
        ordering = ["-created_at"]

    def __str__(self):
        return f"Inference Request {self.id} - {self.inference_type} ({self.status})"
