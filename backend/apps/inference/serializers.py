from rest_framework import serializers
from .models import InferenceRequest


class InferenceRequestSerializer(serializers.ModelSerializer):
    class Meta:
        model = InferenceRequest
        fields = [
            "id",
            "inference_type",
            "payload",
            "status",
            "response",
            "error_details",
            "created_at",
            "updated_at",
        ]
        read_only_fields = [
            "id",
            "status",
            "response",
            "error_details",
            "created_at",
            "updated_at",
        ]
