from django.db import models
from django.utils.text import slugify

# Create your models here.

class LLMModel(models.Model):
    name = models.CharField(max_length=255, unique=True, help_text="Name of the LLM model (e.g. qwen3-8b)")
    slug = models.SlugField(max_length=255, unique=True, help_text="URL-friendly version of the name")
    base_url = models.URLField(help_text="Base URL for the LLM API (e.g. http://localhost:1234/v1)")
    created_at = models.DateTimeField(auto_now_add=True)
    updated_at = models.DateTimeField(auto_now=True)
    is_active = models.BooleanField(default=True)

    class Meta:
        verbose_name = "LLM Model"
        verbose_name_plural = "LLM Models"
        ordering = ['-created_at']

    def save(self, *args, **kwargs):
        if not self.slug:
            self.slug = slugify(self.name)
        super().save(*args, **kwargs)

    def __str__(self):
        return f"{self.name} ({self.base_url})"


class ImageGenModel(models.Model):
    SERVICE_TYPES = [
        ('FLUX_NIM', 'Flux Nim'),
        ('INVOKEAI', 'InvokeAI'),
        ('COMFYUI', 'ComfyUI'),
    ]

    name = models.CharField(max_length=255, unique=True, help_text="Name of the image generation service")
    slug = models.SlugField(max_length=255, unique=True, help_text="URL-friendly identifier for the service")
    base_url = models.URLField(help_text="Base URL for the image generation API")
    service_type = models.CharField(max_length=20, choices=SERVICE_TYPES, help_text="Type of image generation service")
    created_at = models.DateTimeField(auto_now_add=True)
    updated_at = models.DateTimeField(auto_now=True)
    is_active = models.BooleanField(default=True)

    class Meta:
        verbose_name = "Image Generation Model"
        verbose_name_plural = "Image Generation Models"
        ordering = ['-created_at']

    def __str__(self):
        return f"{self.name} ({self.get_service_type_display()})"
