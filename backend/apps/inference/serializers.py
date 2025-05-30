from rest_framework import serializers
from .models import InferenceRequest
from apps.services.models import TTSService


class InferenceRequestSerializer(serializers.ModelSerializer):
    generated_image = serializers.SerializerMethodField()
    input_image = serializers.CharField(required=False, allow_null=True, allow_blank=True)
    tts_service = serializers.PrimaryKeyRelatedField(queryset=TTSService.objects.all(), required=False, allow_null=True)
    speech_output = serializers.FileField(required=False, allow_null=True)
    speech_input = serializers.FileField(required=False, allow_null=True)

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
            "input_image",
            "tts_service",
            "speech_output",
            "speech_input",
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
