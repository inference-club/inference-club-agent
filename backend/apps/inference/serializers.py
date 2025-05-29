from rest_framework import serializers
from .models import InferenceRequest


class InferenceRequestSerializer(serializers.ModelSerializer):
    generated_image = serializers.SerializerMethodField()

    class Meta:
        model = InferenceRequest
        fields = [
            "id",
            "inference_type",
            "payload",
            "status",
            "response",
            "error_details",
            "generated_image",
            "created_at",
            "updated_at",
        ]
        read_only_fields = [
            "id",
            "status",
            "response",
            "error_details",
            "generated_image",
            "created_at",
            "updated_at",
        ]

    def get_generated_image(self, obj):
        if obj.generated_image:
            return obj.generated_image.url
        return None
