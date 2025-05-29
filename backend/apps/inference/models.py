from django.db import models
from django.utils import timezone
from apps.services.models import ImageGenModel

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
    image_gen_service = models.ForeignKey(
        ImageGenModel, null=True, blank=True, on_delete=models.SET_NULL, related_name='inference_requests'
    )
    created_at = models.DateTimeField(default=timezone.now)
    updated_at = models.DateTimeField(auto_now=True)

    class Meta:
        ordering = ["-created_at"]

    def __str__(self):
        return f"Inference Request {self.id} - {self.inference_type} ({self.status})"
