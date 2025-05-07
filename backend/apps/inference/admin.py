from django.contrib import admin
from django import forms
from django.utils.html import format_html
import json
from .models import InferenceRequest


class InferenceRequestAdminForm(forms.ModelForm):
    formatted_response = forms.CharField(
        widget=forms.Textarea(
            attrs={
                "rows": 20,
                "style": "font-family: monospace;",
                "readonly": "readonly",
            }
        ),
        required=False,
    )

    formatted_payload = forms.CharField(
        widget=forms.Textarea(
            attrs={
                "rows": 20,
                "style": "font-family: monospace;",
            }
        ),
        required=False,
    )

    class Meta:
        model = InferenceRequest
        fields = "__all__"

    def __init__(self, *args, **kwargs):
        super().__init__(*args, **kwargs)
        if self.instance:
            if self.instance.response:
                self.initial["formatted_response"] = json.dumps(
                    self.instance.response, indent=2
                )
            if self.instance.payload:
                self.initial["formatted_payload"] = json.dumps(
                    self.instance.payload, indent=2
                )

    def clean(self):
        cleaned_data = super().clean()
        formatted_payload = cleaned_data.get("formatted_payload")
        if formatted_payload:
            try:
                # Parse the formatted JSON and update the actual payload field
                cleaned_data["payload"] = json.loads(formatted_payload)
            except json.JSONDecodeError:
                self.add_error("formatted_payload", "Invalid JSON format")
        return cleaned_data


@admin.register(InferenceRequest)
class InferenceRequestAdmin(admin.ModelAdmin):
    form = InferenceRequestAdminForm
    list_display = (
        "id",
        "inference_type",
        "status",
        "created_at",
        "updated_at",
        "display_image",
    )
    list_filter = ("inference_type", "status")
    search_fields = ("id", "inference_type", "status")
    readonly_fields = ("created_at", "updated_at", "display_image")
    fieldsets = (
        (
            "Request Information",
            {"fields": ("inference_type", "status", "formatted_payload")},
        ),
        (
            "Response Information",
            {
                "fields": (
                    "formatted_response",
                    "error_details",
                    "generated_image",
                    "display_image",
                )
            },
        ),
        ("Timestamps", {"fields": ("created_at", "updated_at")}),
    )

    def display_image(self, obj):
        if obj.generated_image:
            return format_html(
                '<img src="{}" width="150" height="150" style="object-fit: contain;" />',
                obj.generated_image.url,
            )
        return "No image generated"

    display_image.short_description = "Generated Image"

    def get_fieldsets(self, request, obj=None):
        fieldsets = super().get_fieldsets(request, obj)
        # Remove the original response and payload fields from the form
        for fieldset in fieldsets:
            if "fields" in fieldset[1]:
                fields = list(fieldset[1]["fields"])
                if "response" in fields:
                    fields.remove("response")
                if "payload" in fields:
                    fields.remove("payload")
                fieldset[1]["fields"] = tuple(fields)
        return fieldsets
